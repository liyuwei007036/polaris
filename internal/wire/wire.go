// Package wire implements the agent<->master transport: a persistent TCP
// connection authenticated and encrypted with the Noise Protocol Framework
// (Noise_XK, raw Curve25519 keys — no X.509 certificates, no HTTP). Messages
// are binary: a small fixed header plus a gob-encoded body, chunked to fit
// Noise's per-message size limit and reassembled on the receiving end.
package wire

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/flynn/noise"
	"golang.org/x/crypto/curve25519"
)

const KeySize = 32

// maxPlaintextChunk stays safely under noise.MaxMsgLen (65535), leaving room
// for the 16-byte AEAD tag added on encryption.
const maxPlaintextChunk = 60000

// maxLogicalMessageBytes bounds a single logical message (header-declared
// size) against a malicious or buggy peer forcing unbounded allocation.
const maxLogicalMessageBytes = 16 * 1024 * 1024

// Keypair is a raw Curve25519 identity: the WireGuard-style trust model used
// in place of a CA-issued certificate. The public key is what gets pinned
// (by the master, once approved; by the agent, out of band via config).
type Keypair struct {
	Public  [KeySize]byte
	Private [KeySize]byte
}

// GenerateKeypair creates a fresh identity keypair.
func GenerateKeypair() (Keypair, error) {
	kp, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return Keypair{}, fmt.Errorf("generate keypair: %w", err)
	}
	var out Keypair
	copy(out.Public[:], kp.Public)
	copy(out.Private[:], kp.Private)
	return out, nil
}

// KeypairFromPrivate derives the public half of a previously-generated
// private key (used to reload a persisted identity without storing the
// public key separately).
func KeypairFromPrivate(private [KeySize]byte) (Keypair, error) {
	public, err := curve25519.X25519(private[:], curve25519.Basepoint)
	if err != nil {
		return Keypair{}, fmt.Errorf("derive public key: %w", err)
	}
	var out Keypair
	out.Private = private
	copy(out.Public[:], public)
	return out, nil
}

func cipherSuite() noise.CipherSuite {
	return noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
}

// Conn is a Noise-encrypted, length-framed binary connection established
// after a successful handshake.
type Conn struct {
	raw  net.Conn
	send *noise.CipherState
	recv *noise.CipherState
}

// DialXK performs the Noise_XK handshake as the initiator (the agent side).
// The initiator must already know the responder's (master's) static public
// key in advance — that is the one piece of trust the operator configures
// out of band, analogous to pinning a CA today.
func DialXK(raw net.Conn, local Keypair, remoteStaticPub [KeySize]byte) (*Conn, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cipherSuite(),
		Pattern:       noise.HandshakeXK,
		Initiator:     true,
		StaticKeypair: noise.DHKey{Private: local.Private[:], Public: local.Public[:]},
		PeerStatic:    remoteStaticPub[:],
	})
	if err != nil {
		return nil, fmt.Errorf("init handshake: %w", err)
	}
	msg1, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("build handshake message 1: %w", err)
	}
	if err := writeRaw(raw, msg1); err != nil {
		return nil, err
	}
	msg2, err := readRaw(raw)
	if err != nil {
		return nil, err
	}
	if _, _, _, err := hs.ReadMessage(nil, msg2); err != nil {
		return nil, fmt.Errorf("read handshake message 2: %w", err)
	}
	msg3, csSend, csRecv, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("build handshake message 3: %w", err)
	}
	if err := writeRaw(raw, msg3); err != nil {
		return nil, err
	}
	return &Conn{raw: raw, send: csSend, recv: csRecv}, nil
}

// AcceptXK performs the Noise_XK handshake as the responder (the master
// side). Unlike the initiator, the responder does not need to know the
// peer's static key in advance — it is revealed during the handshake itself
// and returned here so the caller can decide whether to trust it (look it up
// against the set of approved nodes), the same way TLS client-certificate
// verification worked before.
func AcceptXK(raw net.Conn, local Keypair) (*Conn, [KeySize]byte, error) {
	var peerStatic [KeySize]byte
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cipherSuite(),
		Pattern:       noise.HandshakeXK,
		Initiator:     false,
		StaticKeypair: noise.DHKey{Private: local.Private[:], Public: local.Public[:]},
	})
	if err != nil {
		return nil, peerStatic, fmt.Errorf("init handshake: %w", err)
	}
	msg1, err := readRaw(raw)
	if err != nil {
		return nil, peerStatic, err
	}
	if _, _, _, err := hs.ReadMessage(nil, msg1); err != nil {
		return nil, peerStatic, fmt.Errorf("read handshake message 1: %w", err)
	}
	msg2, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, peerStatic, fmt.Errorf("build handshake message 2: %w", err)
	}
	if err := writeRaw(raw, msg2); err != nil {
		return nil, peerStatic, err
	}
	msg3, err := readRaw(raw)
	if err != nil {
		return nil, peerStatic, err
	}
	_, csRecv, csSend, err := hs.ReadMessage(nil, msg3)
	if err != nil {
		return nil, peerStatic, fmt.Errorf("read handshake message 3: %w", err)
	}
	copy(peerStatic[:], hs.PeerStatic())
	return &Conn{raw: raw, send: csSend, recv: csRecv}, peerStatic, nil
}

// WriteMessage sends one logical message: msgType tags what Body holds
// (interpreted by the caller, see message.go), chunked into individually
// Noise-encrypted pieces since a single Noise message is capped at 65535
// bytes.
func (c *Conn) WriteMessage(msgType byte, body []byte) error {
	if len(body) > maxLogicalMessageBytes {
		return errors.New("wire: message body exceeds maximum size")
	}
	var header [9]byte
	header[0] = msgType
	binary.BigEndian.PutUint64(header[1:], uint64(len(body)))
	if err := c.writeChunk(header[:]); err != nil {
		return fmt.Errorf("write message header: %w", err)
	}
	for offset := 0; offset < len(body); offset += maxPlaintextChunk {
		end := offset + maxPlaintextChunk
		if end > len(body) {
			end = len(body)
		}
		if err := c.writeChunk(body[offset:end]); err != nil {
			return fmt.Errorf("write message chunk: %w", err)
		}
	}
	return nil
}

// ReadMessage reads one logical message written by WriteMessage.
func (c *Conn) ReadMessage() (msgType byte, body []byte, err error) {
	header, err := c.readChunk()
	if err != nil {
		return 0, nil, fmt.Errorf("read message header: %w", err)
	}
	if len(header) != 9 {
		return 0, nil, errors.New("wire: malformed message header")
	}
	msgType = header[0]
	total := binary.BigEndian.Uint64(header[1:])
	if total > maxLogicalMessageBytes {
		return 0, nil, errors.New("wire: message body exceeds maximum size")
	}
	body = make([]byte, 0, total)
	for uint64(len(body)) < total {
		chunk, err := c.readChunk()
		if err != nil {
			return 0, nil, fmt.Errorf("read message chunk: %w", err)
		}
		body = append(body, chunk...)
	}
	return msgType, body, nil
}

func (c *Conn) writeChunk(plaintext []byte) error {
	ciphertext, err := c.send.Encrypt(nil, nil, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt chunk: %w", err)
	}
	return writeRaw(c.raw, ciphertext)
}

func (c *Conn) readChunk() ([]byte, error) {
	ciphertext, err := readRaw(c.raw)
	if err != nil {
		return nil, err
	}
	plaintext, err := c.recv.Decrypt(nil, nil, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt chunk: %w", err)
	}
	return plaintext, nil
}

func (c *Conn) Close() error                       { return c.raw.Close() }
func (c *Conn) SetDeadline(t time.Time) error       { return c.raw.SetDeadline(t) }
func (c *Conn) SetReadDeadline(t time.Time) error   { return c.raw.SetReadDeadline(t) }
func (c *Conn) RemoteAddr() net.Addr                { return c.raw.RemoteAddr() }

// writeRaw/readRaw frame the (already Noise-encrypted) chunks on the wire
// with a plain 4-byte big-endian length prefix — this length is ciphertext
// size, not sensitive, exactly like a TLS record length.
func writeRaw(conn net.Conn, b []byte) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
	if _, err := conn.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("write frame length: %w", err)
	}
	if _, err := conn.Write(b); err != nil {
		return fmt.Errorf("write frame body: %w", err)
	}
	return nil
}

func readRaw(conn net.Conn) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read frame length: %w", err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n > maxLogicalMessageBytes {
		return nil, errors.New("wire: frame exceeds maximum size")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, fmt.Errorf("read frame body: %w", err)
	}
	return buf, nil
}

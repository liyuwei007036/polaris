package wire

import (
	"bytes"
	"testing"
	"time"
)

func TestKCPHandshakeAndFraming(t *testing.T) {
	masterKP, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	agentKP, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}

	ln, err := ListenKCP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenKCP error: %v", err)
	}
	defer ln.Close()

	type result struct {
		conn       *Conn
		peerStatic [KeySize]byte
		err        error
	}
	serverResult := make(chan result, 1)

	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverResult <- result{err: err}
			return
		}
		conn, peer, err := AcceptXK(raw, masterKP)
		serverResult <- result{conn, peer, err}
	}()

	clientRaw, err := DialKCP(ln.Addr().String())
	if err != nil {
		t.Fatalf("DialKCP error: %v", err)
	}
	defer clientRaw.Close()

	clientConn, err := DialXK(clientRaw, agentKP, masterKP.Public)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	defer clientConn.Close()

	sr := <-serverResult
	if sr.err != nil {
		t.Fatalf("server handshake: %v", sr.err)
	}
	defer sr.conn.Close()

	if sr.peerStatic != agentKP.Public {
		t.Fatalf("server saw wrong peer static key: got %x want %x", sr.peerStatic, agentKP.Public)
	}

	// 1. Test small message
	st := Status{AgentVersion: "kcp-test", OS: "linux", Architecture: "amd64"}
	body, err := Encode(st)
	if err != nil {
		t.Fatal(err)
	}

	writeErr := make(chan error, 1)
	go func() {
		writeErr <- clientConn.WriteMessage(MsgStatus, body)
	}()

	msgType, gotBody, err := sr.conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write message: %v", err)
	}
	if msgType != MsgStatus {
		t.Fatalf("got msg type %d want %d", msgType, MsgStatus)
	}
	var decoded Status
	if err := Decode(gotBody, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.AgentVersion != st.AgentVersion {
		t.Fatalf("got agent version %q want %q", decoded.AgentVersion, st.AgentVersion)
	}

	// 2. Test large message spanning multiple chunks (>60000 bytes)
	bigPayload := bytes.Repeat([]byte("KCP_FEC_LARGE_PAYLOAD_TEST_1234567890"), 3000) // ~108 KB
	bigPush := ConnectionsPush{
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
		Connections: []ConnectionInfo{
			{ID: "conn-1", Host: string(bigPayload)},
		},
	}
	bigBody, err := Encode(bigPush)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		writeErr <- sr.conn.WriteMessage(MsgConnections, bigBody)
	}()

	gotType, gotBigBody, err := clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("read large message: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write large message: %v", err)
	}
	if gotType != MsgConnections {
		t.Fatalf("got msg type %d want %d", gotType, MsgConnections)
	}
	var gotPush ConnectionsPush
	if err := Decode(gotBigBody, &gotPush); err != nil {
		t.Fatalf("decode large push: %v", err)
	}
	if len(gotPush.Connections) != 1 || gotPush.Connections[0].Host != string(bigPayload) {
		t.Fatal("large payload content mismatch")
	}
}

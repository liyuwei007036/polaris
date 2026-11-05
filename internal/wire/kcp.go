package wire

import (
	"net"

	kcp "github.com/xtaci/kcp-go/v5"
)

// KCP Reed-Solomon FEC parameters: 10 data shards, 3 parity shards.
// Tolerates ~23% random packet loss with zero retransmission delay.
const (
	KCPDataShards   = 10
	KCPParityShards = 3
)

// ConfigureKCP applies low-latency and FEC-optimal settings to a KCP session,
// automatically tuning MTU based on dynamic Path MTU discovery.
func ConfigureKCP(sess *kcp.UDPSession) {
	if raddr := sess.RemoteAddr(); raddr != nil {
		if udpMTU, err := DetectUDPMTU(raddr); err == nil && udpMTU > 0 {
			sess.SetMtu(udpMTU)
		}
	}
	sess.SetStreamMode(true)
	sess.SetWriteDelay(false)
	sess.SetACKNoDelay(true)
	sess.SetNoDelay(1, 10, 2, 1)
	sess.SetWindowSize(128, 128)
	sess.SetReadBuffer(4 * 1024 * 1024)
	sess.SetWriteBuffer(4 * 1024 * 1024)
}

// KCPListener wraps *kcp.Listener to automatically configure accepted sessions.
type KCPListener struct {
	*kcp.Listener
}

func (l *KCPListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if sess, ok := conn.(*kcp.UDPSession); ok {
		ConfigureKCP(sess)
	}
	return conn, nil
}

// DialKCP dials a KCP connection to raddr with Reed-Solomon FEC and dynamic MTU tuning.
func DialKCP(raddr string) (*kcp.UDPSession, error) {
	sess, err := kcp.DialWithOptions(raddr, nil, KCPDataShards, KCPParityShards)
	if err != nil {
		return nil, err
	}
	ConfigureKCP(sess)
	return sess, nil
}

// ListenKCP creates a KCP listener on laddr with Reed-Solomon FEC.
func ListenKCP(laddr string) (net.Listener, error) {
	ln, err := kcp.ListenWithOptions(laddr, nil, KCPDataShards, KCPParityShards)
	if err != nil {
		return nil, err
	}
	return &KCPListener{Listener: ln}, nil
}

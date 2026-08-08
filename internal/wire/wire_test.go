package wire

import (
	"net"
	"testing"
)

func TestHandshakeAndFraming(t *testing.T) {
	masterKP, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	agentKP, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	clientRaw, serverRaw := net.Pipe()
	defer clientRaw.Close()
	defer serverRaw.Close()

	type result struct {
		conn       *Conn
		peerStatic [KeySize]byte
		err        error
	}
	serverResult := make(chan result, 1)
	go func() {
		conn, peer, err := AcceptXK(serverRaw, masterKP)
		serverResult <- result{conn, peer, err}
	}()

	clientConn, err := DialXK(clientRaw, agentKP, masterKP.Public)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	sr := <-serverResult
	if sr.err != nil {
		t.Fatalf("server handshake: %v", sr.err)
	}
	if sr.peerStatic != agentKP.Public {
		t.Fatalf("server saw wrong peer static key: got %x want %x", sr.peerStatic, agentKP.Public)
	}

	// Small message.
	st := Status{AgentVersion: "dev", OS: "linux", Architecture: "arm64", SingBoxConfigHash: "singbox-hash", NginxConfigHash: "nginx-hash", Capabilities: map[string]string{"systemd": "true"},
		ForeignStreamListens: []StreamListen{{Address: "0.0.0.0", Port: 443, File: "/etc/nginx/nginx.conf"}}}
	body, err := Encode(st)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := make(chan error, 1)
	go func() { writeErr <- clientConn.WriteMessage(MsgStatus, body) }()
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
	var gotSt Status
	if err := Decode(gotBody, &gotSt); err != nil {
		t.Fatal(err)
	}
	if gotSt.AgentVersion != "dev" || gotSt.SingBoxConfigHash != "singbox-hash" || gotSt.NginxConfigHash != "nginx-hash" || gotSt.Capabilities["systemd"] != "true" {
		t.Fatalf("unexpected decoded status: %#v", gotSt)
	}
	if len(gotSt.ForeignStreamListens) != 1 || gotSt.ForeignStreamListens[0] != (StreamListen{Address: "0.0.0.0", Port: 443, File: "/etc/nginx/nginx.conf"}) {
		t.Fatalf("unexpected decoded foreign stream listens: %#v", gotSt.ForeignStreamListens)
	}

	// Large message spanning multiple Noise chunks (bigger than
	// maxPlaintextChunk), to exercise the chunking/reassembly path.
	var conns []ConnectionInfo
	for i := 0; i < 3000; i++ {
		conns = append(conns, ConnectionInfo{ID: "conn-id-with-some-length-000000", Source: "10.0.0.1:12345", Destination: "1.1.1.1:443", Network: "tcp", Upload: 123, Download: 456})
	}
	push := ConnectionsPush{CollectedAt: "now", Connections: conns}
	bigBody, err := Encode(push)
	if err != nil {
		t.Fatal(err)
	}
	if len(bigBody) <= maxPlaintextChunk {
		t.Fatalf("test fixture too small to exercise chunking: %d bytes", len(bigBody))
	}
	go func() { writeErr <- sr.conn.WriteMessage(MsgConnections, bigBody) }()
	msgType, gotBig, err := clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("read large message: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write large message: %v", err)
	}
	if msgType != MsgConnections {
		t.Fatalf("got msg type %d want %d", msgType, MsgConnections)
	}
	var gotPush ConnectionsPush
	if err := Decode(gotBig, &gotPush); err != nil {
		t.Fatal(err)
	}
	if len(gotPush.Connections) != len(conns) {
		t.Fatalf("got %d connections want %d", len(gotPush.Connections), len(conns))
	}
}

func TestAcceptXKRejectsWrongPeer(t *testing.T) {
	masterKP, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	wrongExpectedMasterKey, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	agentKP, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	clientRaw, serverRaw := net.Pipe()
	defer clientRaw.Close()
	defer serverRaw.Close()

	serverErr := make(chan error, 1)
	go func() {
		_, _, err := AcceptXK(serverRaw, masterKP)
		// A real accept loop must close the connection on handshake failure —
		// otherwise the initiator blocks forever waiting for a reply that
		// will never come, exactly as this test would without it.
		serverRaw.Close()
		serverErr <- err
	}()

	// Agent pins the WRONG master public key — handshake must fail on both sides.
	_, err = DialXK(clientRaw, agentKP, wrongExpectedMasterKey.Public)
	if err == nil {
		t.Fatal("expected handshake failure when agent pins the wrong master key")
	}
	if err := <-serverErr; err == nil {
		t.Fatal("expected server-side handshake failure too")
	}
}

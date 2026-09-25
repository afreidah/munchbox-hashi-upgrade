// -------------------------------------------------------------------------------
// SSH Execution Tests - Against an In-Process Server
//
// Author: Alex Freidah
//
// Runs the client against a real SSH server built in the test, because the parts
// worth testing are the parts that only exist once a connection is up: which
// credential the host accepted, what it received, what came back, and how a
// cancelled command ends.
//
// The server trusts any key it is offered and records the first one, which is
// how the ordering of the auth methods is asserted rather than assumed.
// -------------------------------------------------------------------------------

package ssh

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// reply is what the test server does with a command.
type reply struct {
	stdout  string
	stderr  string
	code    int
	started chan struct{} // closed once the command has been received
	block   chan struct{} // when set, the server waits on it before replying
}

// server is an SSH server listening on the loopback interface for the length of
// a test.
type server struct {
	target Target
	reply  reply

	mu       sync.Mutex
	commands []string
	offered  ssh.PublicKey
}

// serve starts a server presenting a host certificate signed by ca, and returns
// it once it is accepting connections.
func serve(t *testing.T, ca ssh.Signer, r reply) *server {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	host, port := split(t, listener.Addr().String())
	srv := &server{target: Target{Host: host, Port: port, User: "root"}, reply: r}

	cfg := &ssh.ServerConfig{PublicKeyCallback: srv.accept}
	cfg.AddHostKey(hostCertSigner(t, ca, host))
	go srv.listen(listener, cfg)

	return srv
}

// split breaks a listener address into the parts a Target carries.
func split(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %s: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %s: %v", portStr, err)
	}
	return host, port
}

// hostCertSigner returns a signer presenting a host certificate for principal,
// signed by ca, which is what the client verifies against.
func hostCertSigner(t *testing.T, ca ssh.Signer, principal string) ssh.Signer {
	t.Helper()
	signer, _ := testSigner(t)
	cert := &ssh.Certificate{
		Key:             signer.PublicKey(),
		CertType:        ssh.HostCert,
		ValidPrincipals: []string{principal},
		ValidBefore:     ssh.CertTimeInfinity,
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatalf("sign host cert: %v", err)
	}
	certSigner, err := ssh.NewCertSigner(cert, signer)
	if err != nil {
		t.Fatalf("host cert signer: %v", err)
	}
	return certSigner
}

// accept records the first credential offered and admits it.
func (s *server) accept(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.offered == nil {
		s.offered = key
	}
	return &ssh.Permissions{}, nil
}

// listen serves connections until the listener is closed.
func (s *server) listen(listener net.Listener, cfg *ssh.ServerConfig) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go s.handshake(conn, cfg)
	}
}

// handshake completes the SSH handshake and serves the session channels.
func (s *server) handshake(conn net.Conn, cfg *ssh.ServerConfig) {
	defer func() { _ = conn.Close() }()

	server, channels, requests, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = server.Close() }()
	go ssh.DiscardRequests(requests)

	for channel := range channels {
		if channel.ChannelType() != "session" {
			_ = channel.Reject(ssh.UnknownChannelType, "sessions only")
			continue
		}
		accepted, requests, err := channel.Accept()
		if err != nil {
			return
		}
		go s.session(accepted, requests)
	}
}

// session answers one exec request with the configured reply.
func (s *server) session(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()

	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			return
		}
		_ = request.Reply(true, nil)

		s.mu.Lock()
		s.commands = append(s.commands, payload.Command)
		s.mu.Unlock()

		if s.reply.started != nil {
			close(s.reply.started)
		}
		if s.reply.block != nil {
			<-s.reply.block
		}
		_, _ = io.WriteString(channel, s.reply.stdout)
		_, _ = io.WriteString(channel.Stderr(), s.reply.stderr)
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{uint32(s.reply.code)}))
		return
	}
}

// received returns the commands the server was asked to run.
func (s *server) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// credential returns the first credential the server was offered.
func (s *server) credential() ssh.PublicKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offered
}

// client builds a client holding a certificate signed by ca and trusting ca as
// a host authority.
func client(t *testing.T, ca ssh.Signer, withCert bool) *Client {
	t.Helper()
	signer, keyPEM := testSigner(t)

	cfg := Config{
		KeyPath:    writeTemp(t, "id", keyPEM),
		HostCAPath: writeTemp(t, "ca.pub", ssh.MarshalAuthorizedKey(ca.PublicKey())),
	}
	if withCert {
		cert := signUserCert(t, ca, signer, "root")
		cfg.CertPath = writeTemp(t, "id-cert.pub", ssh.MarshalAuthorizedKey(cert))
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestRun(t *testing.T) {
	ca, _ := testSigner(t)
	srv := serve(t, ca, reply{stdout: "converged\n", stderr: "warning\n"})

	conn, err := client(t, ca, true).Connect(srv.target)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var streamed strings.Builder
	result, err := conn.Run(t.Context(), "cinc-client", &streamed)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.OK() {
		t.Errorf("exit %d, want a clean exit", result.Code)
	}
	for _, want := range []string{"converged", "warning"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("captured output %q is missing %q", result.Output, want)
		}
		if !strings.Contains(streamed.String(), want) {
			t.Errorf("streamed output %q is missing %q", streamed.String(), want)
		}
	}
	if got := srv.received(); len(got) != 1 || got[0] != "cinc-client" {
		t.Errorf("server received %q, want [cinc-client]", got)
	}
}

// A converge that fails reports a status the caller interprets. The client's job
// is to carry it back, not to decide that a non-zero exit is a transport error.
func TestRunNonZeroExit(t *testing.T) {
	ca, _ := testSigner(t)
	srv := serve(t, ca, reply{stdout: "failed\n", code: 3})

	conn, err := client(t, ca, true).Connect(srv.target)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	result, err := conn.Run(t.Context(), "cinc-client", nil)
	if err != nil {
		t.Fatalf("a non-zero exit should not be an error: %v", err)
	}
	if result.Code != 3 || result.OK() {
		t.Errorf("got exit %d (ok=%v), want 3", result.Code, result.OK())
	}
	if !strings.Contains(result.Output, "failed") {
		t.Errorf("output %q was not captured", result.Output)
	}
}

// The certificate has to be the credential the host accepts. A node whose
// authorized_keys carries a forced command answers the bare key with that
// command instead of the one asked for.
func TestRunOffersTheCertificateFirst(t *testing.T) {
	ca, _ := testSigner(t)

	withCert := serve(t, ca, reply{})
	conn, err := client(t, ca, true).Connect(withCert.target)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_ = conn.Close()
	if _, ok := withCert.credential().(*ssh.Certificate); !ok {
		t.Errorf("host was offered %T first, want a certificate", withCert.credential())
	}

	keyOnly := serve(t, ca, reply{})
	conn, err = client(t, ca, false).Connect(keyOnly.target)
	if err != nil {
		t.Fatalf("Connect without a certificate: %v", err)
	}
	_ = conn.Close()
	if _, isCert := keyOnly.credential().(*ssh.Certificate); isCert {
		t.Error("a client holding no certificate offered one")
	}
}

func TestRunSudo(t *testing.T) {
	ca, _ := testSigner(t)
	srv := serve(t, ca, reply{})
	target := srv.target
	target.User, target.Sudo = "ubuntu", true

	conn, err := client(t, ca, true).Connect(target)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Run(t.Context(), "cinc-client", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := srv.received(); len(got) != 1 || got[0] != "sudo cinc-client" {
		t.Errorf("server received %q, want [sudo cinc-client]", got)
	}
}

// Cancelling has to end the call. A converge that hangs is the case this exists
// for, so the command must not outlive the context that asked for it.
func TestRunCancelled(t *testing.T) {
	ca, _ := testSigner(t)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	srv := serve(t, ca, reply{stdout: "never arrives\n", started: started, block: release})

	conn, err := client(t, ca, true).Connect(srv.target)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()

	if _, err := conn.Run(ctx, "cinc-client", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestConnectRejectsAnUntrustedHost(t *testing.T) {
	ca, _ := testSigner(t)
	stranger, _ := testSigner(t)
	srv := serve(t, stranger, reply{})

	if _, err := client(t, ca, true).Connect(srv.target); err == nil {
		t.Error("a host key signed by an unknown authority should be refused")
	}
}

// Port 1 is reserved and closed, so the dial is refused at once. It also covers
// the default-port branch, since a target with no port becomes 22.
func TestConnectDialFails(t *testing.T) {
	ca, _ := testSigner(t)
	c := client(t, ca, true)

	if _, err := c.Connect(Target{Host: "127.0.0.1", Port: 1, User: "root"}); err == nil {
		t.Error("expected a dial error against a closed port")
	}
	if _, err := c.Connect(Target{Host: "127.0.0.1", User: "root"}); err == nil {
		t.Error("expected a dial error on the default port")
	}
}

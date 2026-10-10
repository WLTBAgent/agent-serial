package serial

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// openSSHStream dials an SSH target, authenticates with a private key
// (preferred) or password, and opens a PTY+shell — or runs Target.Command
// when set (console servers that expose lines via a command). Host keys are
// pinned on first use (TOFU) in knownHostsFile and verified thereafter.
func openSSHStream(target Target, knownHostsFile string) (ByteStream, error) {
	auth, err := sshAuth(target)
	if err != nil {
		return nil, err
	}
	hostKeyCallback, err := tofuHostKeyCallback(knownHostsFile)
	if err != nil {
		return nil, err
	}

	client, err := ssh.Dial("tcp", target.Port, &ssh.ClientConfig{
		User:            target.Username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		Timeout:         dialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", target.Port, err)
	}

	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("ssh session: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, err
	}

	if target.Command != "" {
		if err := session.Start(target.Command); err != nil {
			session.Close()
			client.Close()
			return nil, fmt.Errorf("ssh exec %q: %w", target.Command, err)
		}
	} else {
		if err := session.RequestPty("xterm-256color", 80, 24, ssh.TerminalModes{}); err != nil {
			session.Close()
			client.Close()
			return nil, fmt.Errorf("ssh pty request: %w", err)
		}
		if err := session.Shell(); err != nil {
			session.Close()
			client.Close()
			return nil, fmt.Errorf("ssh shell: %w", err)
		}
	}

	return &sshStream{
		client:  client,
		session: session,
		stdin:   stdin,
		out:     newMergeReader(stdout, stderr),
	}, nil
}

type sshStream struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	out     io.Reader
}

func (s *sshStream) Read(p []byte) (int, error) { return s.out.Read(p) }

func (s *sshStream) Write(p []byte) (int, error) { return s.stdin.Write(p) }

func (s *sshStream) Close() error {
	s.session.Close()
	return s.client.Close()
}

func sshAuth(target Target) ([]ssh.AuthMethod, error) {
	if target.KeyPath != "" {
		key, err := os.ReadFile(target.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read ssh key: %w", err)
		}
		var signer ssh.Signer
		if target.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(target.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("parse ssh key %s: %w", target.KeyPath, err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	return []ssh.AuthMethod{ssh.Password(target.Password)}, nil
}

// tofuHostKeyCallback returns a host key callback that verifies against the
// known-hosts file and pins unknown hosts on first use. A key that differs
// from the pinned one is a hard error (possible MITM), never silently
// updated.
func tofuHostKeyCallback(file string) (ssh.HostKeyCallback, error) {
	if file == "" {
		return nil, fmt.Errorf("ssh known-hosts file must be configured (--known-hosts)")
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, fmt.Errorf("create known-hosts dir: %w", err)
	}
	// knownhosts.New errors on a missing file; first use pins entries into
	// the empty file we create here.
	if f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
		return nil, fmt.Errorf("create known-hosts %s: %w", file, err)
	} else {
		f.Close()
	}
	verify, err := knownhosts.New(file)
	if err != nil {
		return nil, fmt.Errorf("load known-hosts %s: %w", file, err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) || len(keyErr.Want) > 0 {
			return fmt.Errorf("host key mismatch for %s (possible MITM); if the host legitimately changed, remove its entry from %s", hostname, file)
		}
		// Unknown host: pin it (trust on first use).
		line := fmt.Sprintf("%s %s %s\n",
			knownhosts.Normalize(hostname), key.Type(), base64.StdEncoding.EncodeToString(key.Marshal()))
		f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("pin host key: %w", err)
		}
		defer f.Close()
		if _, err := f.WriteString(line); err != nil {
			return fmt.Errorf("pin host key: %w", err)
		}
		return nil
	}, nil
}

// newMergeReader merges two readers (SSH stdout and stderr) into one,
// closing the pipe once both sources are exhausted.
type mergeReader struct {
	r *io.PipeReader
}

func newMergeReader(a, b io.Reader) io.Reader {
	pr, pw := io.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(pw, a) }()
	go func() { defer wg.Done(); _, _ = io.Copy(pw, b) }()
	go func() {
		wg.Wait()
		pw.Close()
	}()
	return &mergeReader{r: pr}
}

func (m *mergeReader) Read(p []byte) (int, error) { return m.r.Read(p) }

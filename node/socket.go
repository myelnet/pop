package node

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"github.com/rs/zerolog/log"
)

// Shameless copy of tailscale safesocket implementation

// SocketListen returns a listener on unix socket or tcp connect
func SocketListen(path string) (net.Listener, error) {
	return tcpListen(2001)
}

func tcpListen(port uint16) (net.Listener, error) {
	pipe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	return pipe, nil
}

func unixListen(path string) (net.Listener, error) {
	c, err := net.Dial("unix", path)
	if err == nil {
		c.Close()
		return nil, fmt.Errorf("%v: address already in use", path)
	}
	_ = os.Remove(path)

	perm := socketPermissionsForOS()

	sockDir := filepath.Dir(path)
	if _, err := os.Stat(sockDir); os.IsNotExist(err) {
		os.MkdirAll(sockDir, 0755) // best effort

		if perm == 0666 {
			if fi, err := os.Stat(sockDir); err == nil && fi.Mode()&0077 == 0 {
				if err := os.Chmod(sockDir, 0755); err != nil {
					log.Error().Err(err)
				}
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	pipe, err := net.Listen("unix", filepath.Join(home, path))
	if err != nil {
		return nil, err
	}
	os.Chmod(path, perm)
	return pipe, err
}

func socketPermissionsForOS() os.FileMode {
	if runtime.GOOS == "linux" {
		return 0666
	}

	return 0600
}

// SocketConnect can connect to a tcp or unix socket
func SocketConnect() (net.Conn, error) {
	return tcpConnect()
}

func tcpConnect() (net.Conn, error) {
	return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", 2001))
}

func unixConnect() (net.Conn, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	c, err := net.Dial("unix", filepath.Join(home, "hopd.sock"))
	if err != nil {
		return nil, err
	}

	return c, nil
}

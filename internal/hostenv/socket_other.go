//go:build !unix

package hostenv

// SocketGID is unknown where the daemon is not reached through a unix socket.
func SocketGID(string) (int, bool) { return 0, false }

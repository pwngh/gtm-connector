package postgres_test

import "golang.org/x/sys/unix"

// keepIdleOption is the socket option that holds the delay before the first keepalive probe.
const keepIdleOption = unix.TCP_KEEPIDLE

//go:build !windows

package main

import "syscall"

const scorePatchNoBlock = syscall.O_NONBLOCK

//go:build !windows

package service

import "errors"

const TaskName = "Tidal Bridge Host"

var ErrNotInstalled = errors.New("background service registration is implemented for Windows hosts only")

func Install(executable, dataDir string) error { return ErrNotInstalled }
func Uninstall() error                         { return nil }
func Installed() bool                          { return false }
func Start() error                             { return ErrNotInstalled }
func Stop() error                              { return ErrNotInstalled }
func Status() (string, error)                  { return "", ErrNotInstalled }

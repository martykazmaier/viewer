// Copyright (C) 2026 Martin Kazmaier.
// This software may be distributed under the terms of the Q Public License
// version 1.0; see the LICENSE file.

// Command viewer is a Win32 socket (DOOR32) BBS door for EleBBS, Mystic and other
// DOOR32.SYS-capable BBS packages. It displays text and ANSI files (with optional
// baud rate emulation and ANSI music) and browses archives with long file names.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type config struct {
	dropFile string
	handle   int64
	local    bool
	baud     int
	idle     int
	timeLeft int
	width    int
	height   int
	music    string
	graphics string
	file     string
}

const usageText = `VIEWER - Win32 socket file viewer door

Usage: viewer.exe [options] <file>

  -D<path>   DOOR32.SYS file, or the directory that contains it
  -H<n>      Socket handle passed by the BBS (overrides DOOR32.SYS)
  -L         Local mode (sysop console, no socket)
  -B<baud>   Baud rate emulation for file display: 300..115200, 0 = off
  -T<min>    Idle timeout in minutes (default 5, 0 = none)
  -S<c>x<r>  Force terminal size, e.g. -S80x25 (default: auto-detect)
  -M<mode>   ANSI music: pass (default), sync = ESC[|, banana = ESC[N,
             ansi = ESC[M, strip = remove
  -G<mode>   Graphics: auto (default, Sixel if the terminal reports it),
             sixel = always Sixel, ansi = always ANSI half blocks
  -N<n>      Node number (accepted for compatibility, ignored)

Files: text (.txt .nfo .diz ...), ANSI (.ans .asc .ice .mus .ams ...),
images (JPEG, JPEG XL, PNG, GIF, BMP, TIFF, WebP),
archives (ZIP, RAR 4/5, ARJ, LHA/LZH, TAR, plus .gz/.tgz/.bz2 wrappers).
`

var errUsage = errors.New("usage")

func parseArgs(args []string) (config, error) {
	cfg := config{music: "pass", graphics: "auto", idle: 5}
	var rest []string
	opts := true
	for _, a := range args {
		if opts && a == "--" {
			opts = false
			continue
		}
		isOpt := len(a) >= 2 && (a[0] == '-' || (a[0] == '/' && !strings.ContainsAny(a[1:], `/\`)))
		if !opts || !isOpt {
			rest = append(rest, a)
			continue
		}
		v := a[2:]
		num := func() (int, error) {
			n, err := strconv.Atoi(v)
			if err != nil {
				return 0, fmt.Errorf("option %s needs a number", a[:2])
			}
			return n, nil
		}
		var err error
		switch strings.ToUpper(a[1:2]) {
		case "D":
			cfg.dropFile = v
		case "H":
			cfg.handle, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				err = fmt.Errorf("bad socket handle %q", v)
			}
		case "L":
			cfg.local = true
		case "B":
			cfg.baud, err = num()
		case "T":
			cfg.idle, err = num()
		case "N":
		case "S":
			c, r, ok := strings.Cut(strings.ToLower(v), "x")
			cfg.width, _ = strconv.Atoi(c)
			cfg.height, _ = strconv.Atoi(r)
			if !ok || cfg.width < 40 || cfg.height < 10 {
				err = fmt.Errorf("bad screen size %q (use e.g. -S80x25)", v)
			}
		case "M":
			cfg.music = strings.ToLower(v)
			switch cfg.music {
			case "pass", "sync", "banana", "ansi", "strip":
			default:
				err = fmt.Errorf("unknown music mode %q", v)
			}
		case "G":
			cfg.graphics = strings.ToLower(v)
			switch cfg.graphics {
			case "auto", "sixel", "ansi":
			default:
				err = fmt.Errorf("unknown graphics mode %q", v)
			}
		case "?":
			err = errUsage
		default:
			err = fmt.Errorf("unknown option %s", a)
		}
		if err != nil {
			return cfg, err
		}
	}
	cfg.file = strings.Trim(strings.Join(rest, " "), `"`)
	if cfg.file == "" {
		return cfg, errUsage
	}
	return cfg, nil
}

// readDoor32 loads DOOR32.SYS: comm type, handle, baud, BBS, user#, real name,
// alias, security, minutes left, emulation, node.
func readDoor32(cfg *config) error {
	p := cfg.dropFile
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		p = filepath.Join(p, "DOOR32.SYS")
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("cannot read drop file: %v", err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, strings.TrimSpace(sc.Text()))
	}
	if len(lines) < 2 {
		return fmt.Errorf("%s is not a valid DOOR32.SYS", p)
	}
	switch lines[0] {
	case "0":
		cfg.local = true
	case "1":
		return errors.New("serial connections are not supported; use a telnet node")
	}
	if cfg.handle == 0 {
		cfg.handle, _ = strconv.ParseInt(lines[1], 10, 64)
	}
	if len(lines) > 8 {
		cfg.timeLeft, _ = strconv.Atoi(lines[8])
	}
	return nil
}

// logError appends startup failures to viewer.log beside the executable, since
// the BBS window usually truncates or hides stderr.
func logError(err error) {
	exe, e := os.Executable()
	if e != nil {
		return
	}
	f, e := os.OpenFile(filepath.Join(filepath.Dir(exe), "viewer.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if e != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  cmd: %q\r\n    error: %v\r\n", time.Now().Format("2006-01-02 15:04:05"), os.Args, err)
}

func main() {
	cfg, err := parseArgs(os.Args[1:])
	if err != nil {
		if err != errUsage {
			fmt.Fprintln(os.Stderr, "viewer:", err)
		}
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(1)
	}
	if cfg.dropFile != "" {
		if err := readDoor32(&cfg); err != nil {
			fmt.Fprintln(os.Stderr, "viewer:", err)
			logError(err)
			os.Exit(1)
		}
	}
	if cfg.handle == 0 {
		cfg.local = true
	}
	if cfg.local {
		cfg.handle = 0
	}
	s, err := newSession(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "viewer:", err)
		logError(err)
		os.Exit(1)
	}
	os.Exit(s.run(func() { viewPath(s, cfg.file) }))
}

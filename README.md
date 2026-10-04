# Viewer

A DOOR32 (Win32 socket) file viewer door for EleBBS, Mystic and other BBS
packages that support `DOOR32.SYS`. It displays text and ANSI files with
optional baud rate emulation and ANSI music, and browses archives with long
file names using a light bar.

Builds are available for Windows and Linux.

## Features

- **Text viewer**: full-screen scrolling, tab expansion and line wrapping;
  binary files open in a hex view.
- **ANSI viewer**: streams `.ans`, `.asc`, `.ice`, `.mus`, `.ams`, `.drk` and
  `.cia` files, or any file containing ANSI codes. Stops at the end-of-file
  marker before a SAUCE record and shows the SAUCE details on request.
- **Baud rate emulation**: from 300 to 115200 baud, set on the command line or
  changed live. A keypress interrupts slow output without breaking an ANSI
  sequence.
- **ANSI music**: recognises every common form: `ESC[M` (including `ESC[MF`
  and `ESC[MB`), BananaCom `ESC[N` and SyncTERM `ESC[|`, each ending with
  Ctrl-N (0x0E). Music can be passed through, converted to another form for
  the caller's terminal, or removed. In Windows local mode it plays through the
  PC's sound card.
- **Archive browser**: light-bar file list with size, compression ratio, date
  and time, plus a details screen for each file (full long name, folder,
  sizes, method, CRC-32, attributes, host OS, comments). Files inside archives
  can be opened in the matching viewer, including archives inside archives.
- **Long file names**: Unicode names are converted to the BBS character set
  (CP437).
- **Robust connection handling**: telnet control bytes are filtered, the
  caller's screen size is detected automatically, hangups are detected, and
  the BBS's time limit and an idle timeout are enforced.

### Supported archives

| Format | File list | View files inside |
|---|---|---|
| ZIP (incl. ZIP64) | Yes | Stored, Deflate, BZip2 |
| RAR 4 and RAR 5 | Yes | Stored entries only |
| ARJ | Yes | Stored entries only |
| LHA / LZH (header levels 0-2) | Yes | Stored entries only |
| TAR | Yes | Yes |
| `.gz`, `.tgz`, `.bz2`, `.tbz2` | Unpacked first, then viewed or listed | Yes |

7-Zip archives are not supported. Encrypted entries and files split across
volumes are listed but cannot be viewed.

## Download

Prebuilt binaries are in the [`releases`](releases/) folder:

| File | Platform |
|---|---|
| `viewer-win32.exe` | Windows, 32-bit (also runs on 64-bit Windows) |
| `viewer-linux-386` | Linux, 32-bit x86 |
| `viewer-linux-amd64` | Linux, 64-bit x86 |
| `viewer-linux-arm64` | Linux, 64-bit ARM (e.g. Raspberry Pi 4/5) |

## Usage

```
viewer [options] <file>
```

| Option | Meaning |
|---|---|
| `-D<path>` | `DOOR32.SYS` file, or the folder that contains it |
| `-H<n>` | Socket handle passed by the BBS (overrides `DOOR32.SYS`) |
| `-L` | Local mode (sysop console, no socket) |
| `-B<baud>` | Baud rate emulation for file display: 300 to 115200; 0 = off (default) |
| `-T<min>` | Idle timeout in minutes (default 5, 0 = none) |
| `-S<cols>x<rows>` | Force the screen size, e.g. `-S80x25` (default: auto-detect) |
| `-M<mode>` | ANSI music handling (see below) |
| `-N<n>` | Node number (accepted for compatibility, ignored) |

Options use the BBS style with the value attached (`-B2400`, not `-B 2400`).
Everything that isn't an option is taken as the file name, so paths with
spaces work with or without quotes.

If neither `-D` nor `-H` gives a socket, the viewer runs in local mode.

### ANSI music modes

| Mode | Sends to the caller |
|---|---|
| `pass` (default) | Music sequences exactly as they are in the file |
| `sync` | Converted to `ESC[|` (plays in SyncTERM without extra settings) |
| `banana` | Converted to BananaCom `ESC[N` |
| `ansi` | Converted to `ESC[M` |
| `strip` | Music removed, for terminals that would print it as text |

## BBS setup

Set the viewer up as a DOOR32 (socket) door and pass either the drop file or
the socket handle:

```
viewer-win32.exe -D<node drop-file folder> -B2400 -T15 "C:\files\art\Some Long Name.ans"
viewer-win32.exe -H<socket handle> "C:\files\uploads\Some Archive.zip"
```

The macros that insert the node folder or socket handle differ between EleBBS
and Mystic; check your BBS's door command reference.

The viewer reads these `DOOR32.SYS` lines: line 1 (connection type: 0 =
local, 2 = telnet; serial connections are not supported), line 2 (socket
handle) and line 9 (minutes left).

### Launching from another door

The viewer can be started from another door, such as a file listing door.

- **Windows:** the launching door should start the viewer with handle
  inheritance (`CreateProcess` with `bInheritHandles = TRUE`). If it doesn't,
  the viewer copies the socket from its parent process (or a `cmd.exe`
  wrapper, or the BBS) automatically.
- **Linux:** the launching door must leave the socket descriptor open when it
  starts the viewer.
- Don't capture the viewer's standard output: everything is sent to the
  caller over the socket, so captured output is always empty.
- Don't read from the socket while the viewer runs, or the caller's keypresses
  won't reach it. Wait for the viewer to exit, then redraw your screen (the
  viewer clears it on exit).
- Pass the full long file name, quoted.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Caller exited normally (also used for idle or time-limit exits) |
| 1 | Viewer could not start; the reason is in `viewer.log` next to the program |
| 2 | Caller hung up |

## Keys

**Archive list:** Up/Down, PgUp/PgDn, Home/End to move the light bar; Enter
(or Right) for details; `V` to view the file; `C` for the archive comment;
`Q`, Esc or Left to exit.

**Details screen:** Up/Down (or `P`/`N`) for the previous or next file;
Home/End; `V` or Enter to view; `Q`, Esc or Backspace to return to the list.

**Text and hex viewer:** Up/Down, PgUp/PgDn (also Space, `+` and `-`),
Home/End to scroll; `B` to change the baud rate; `Q` or Esc to close.

**After an ANSI file:** Enter, Space, `Q` or Esc to close; `R` to replay;
`B` to change the baud rate; `M` to change the music mode (when the file has
music); `I` for SAUCE information (when present).

## Platform notes

- **Windows:** local mode uses the console with CP437 output and plays ANSI
  music through the sound card.
- **Linux:** the BBS passes the socket as a file descriptor (`DOOR32.SYS` or
  `-H`). Local mode uses stdin/stdout in raw mode, which also works as a
  stdio door. There is no local sound, so ANSI music is always sent on to the
  terminal.

## Building

Requires Go 1.22 or later. On Windows, run:

```
build.cmd
```

This writes all four binaries to `releases/`. To build one target by hand:

```
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=arm64
go build -trimpath -ldflags "-s -w" -o releases\viewer-linux-arm64 .
```

`go test .` runs the archive and ANSI music tests. On Windows with 7-Zip
installed, the test archives are also checked against 7-Zip.

## Source layout

| File | Purpose |
|---|---|
| `main.go` | Command line, `DOOR32.SYS`, startup |
| `session.go` | Connection I/O, key decoding, baud pacing, screen helpers |
| `sys_windows.go`, `sys_linux.go` | Platform sockets, console and sound |
| `view.go` | Picks the right viewer for a file |
| `textview.go` | Text and hex viewer |
| `ansiview.go` | ANSI viewer and SAUCE |
| `music.go` | ANSI music detection, conversion and playback |
| `arcview.go` | Archive light bar and details screen |
| `archive.go`, `arc_zip.go`, `arc_rar.go`, `arc_dos.go` | Archive readers |
| `cp437.go`, `textutil.go` | Character set and text helpers |

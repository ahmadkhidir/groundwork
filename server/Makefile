# Build entry point for the API service.
#
# Exists for one reason: the DuckDB driver needs cgo, and Windows needs different linkage than
# Linux. Encoding that here means nobody has to remember a five-variable incantation, and nobody
# is tempted to bake it into a global `go env -w` where it would silently affect every other Go
# project on the machine.
#
# Linux and macOS need none of this. They link the static libraries the bindings ship with, which
# is also what CI and production do. Everything below the detection line is Windows-only.

GO ?= go

# --- Windows: dynamic linkage against the MSYS2 DuckDB build ------------------------------------
#
# The static libraries bundled in duckdb-go-bindings/windows-amd64 were compiled by a GCC using the
# win32 thread model, which emits emulated-TLS symbol references (__emutls_v._ZSt11__once_call).
# MSYS2's GCC is built --enable-threads=posix --enable-tls, so its libstdc++ defines no such
# symbols and the link fails outright. That is a toolchain ABI mismatch, not a missing dependency:
# no additional package fixes it.
#
# So on Windows we link dynamically against MSYS2's own libduckdb, built by the same toolchain and
# therefore ABI-compatible by construction. The Go source is byte-identical either way — only the
# linkage differs. See IMPLEMENTATION.md "Known divergences".
#
# DUCKDB_DIR must hold DuckDB's OFFICIAL Windows release for the exact version duckdb-go targets,
# not MSYS2's `mingw-w64-ucrt-x86_64-duckdb` package. MSYS2 lags (1.4.4 against the 1.5.5 the driver
# expects) and the driver fails to link against it on missing newer C API symbols.
#
# Worse, MSYS2's copy must not merely be unused — it must be ABSENT. It installs into the compiler's
# default search path as `libduckdb.dll.a`, so the driver's own `#cgo LDFLAGS: -lduckdb` finds it
# even with no -L pointing there. The result links against BOTH DuckDB runtimes at once and binds
# symbols from whichever the loader reaches first. Uninstall it:
#     pacman -R mingw-w64-ucrt-x86_64-duckdb
#
# `-lduckdb` resolves against libduckdb.dll directly (GNU ld links DLLs without an import library),
# which is why the release's duckdb.dll is copied to libduckdb.dll during setup.
ifeq ($(OS),Windows_NT)
  MSYS2_PREFIX ?= C:/msys64/ucrt64
  DUCKDB_DIR ?= $(USERPROFILE)/.local/duckdb/1.5.5
  BUILD_TAGS := duckdb_use_lib
  export CGO_ENABLED := 1
  export CGO_CFLAGS := -I$(DUCKDB_DIR)
  export CGO_LDFLAGS := -L$(DUCKDB_DIR) -lduckdb
  # The binary loads duckdb.dll at run time. Without it on PATH the process exits 127 (or 0xc0000135)
  # with no output whatsoever — a silent failure that resembles a crash, not a missing library.
  export PATH := $(MSYS2_PREFIX)/bin:$(DUCKDB_DIR):$(PATH)
  TAGFLAG := -tags $(BUILD_TAGS)
else
  export CGO_ENABLED := 1
  TAGFLAG :=
endif

.PHONY: build run test test-race vet fmt check doctor

build:
	$(GO) build $(TAGFLAG) ./...

run:
	$(GO) run $(TAGFLAG) ./cmd/api

test:
	$(GO) test $(TAGFLAG) ./...

# The race detector needs cgo too, so it goes through the same flags rather than a bare `go test`.
test-race:
	$(GO) test -race $(TAGFLAG) ./...

vet:
	$(GO) vet $(TAGFLAG) ./...

# Reports the offending files and fails, rather than reformatting behind your back — a target that
# edits your working tree as a side effect of "checking" is a bad neighbour in CI and in review.
# Avoids `tee /dev/stderr`, which does not exist under Git Bash on Windows.
fmt:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# What CI runs. Ordered cheapest-first so an unformatted file fails in a second rather than a minute.
check: fmt vet test

# Diagnoses the toolchain instead of leaving you to interpret a linker error. Run this first when a
# build fails on a new machine.
doctor:
	@echo "GOOS/GOARCH : $$($(GO) env GOOS)/$$($(GO) env GOARCH)"
	@echo "CGO_ENABLED : $$($(GO) env CGO_ENABLED)"
	@echo "CC          : $$($(GO) env CC)"
	@echo "build tags  : $(BUILD_TAGS)"
	@command -v gcc >/dev/null 2>&1 \
		&& echo "gcc         : $$(gcc --version | head -1)" \
		|| echo "gcc         : NOT FOUND — install a C toolchain (see IMPLEMENTATION.md)"
ifeq ($(OS),Windows_NT)
	@test -f "$(DUCKDB_DIR)/libduckdb.dll" \
		&& echo "libduckdb   : $(DUCKDB_DIR)" \
		|| echo "libduckdb   : NOT FOUND at $(DUCKDB_DIR) — see IMPLEMENTATION.md §8"
	@test -f "$(MSYS2_PREFIX)/lib/libduckdb.dll.a" \
		&& echo "CONFLICT    : MSYS2 duckdb is installed and WILL be linked in addition to the" \
		&& echo "              official one. Remove it: pacman -R mingw-w64-ucrt-x86_64-duckdb" \
		|| echo "conflicts   : none"
endif

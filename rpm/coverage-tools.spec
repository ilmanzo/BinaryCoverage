#
# spec file for package coverage-tools
#
# Copyright (c) 2025 SUSE LLC
#
# All modifications and additions to the file contributed by third parties
# remain the property of their copyright owners, unless otherwise agreed
# upon. The license for this file, and modifications and additions to the
# file, is the same license as for the pristine package itself (unless the
# license for the pristine package is not an Open Source License, in which
# case the license is the MIT License). An "Open Source License" is a
# license that conforms to the Open Source Definition (Version 1.9)
# published by the Open Source Initiative.

# Please submit bugfixes or comments via https://bugs.opensuse.org/

%define bversion 0.7.0
%define dname BinaryCoverage-%{bversion}
Name:           coverage-tools
Version:        0.7.0
Release:        0
Summary:        Function-level binary coverage via eBPF (uprobe_multi)
License:        MIT
URL:            https://github.com/ilmanzo/BinaryCoverage
Source0:        %{dname}.tar.gz
Source1:        vendor.tar.gz

# Build-time (pre-generated BPF bindings are shipped in the tarball)
BuildRequires:  go >= 1.26
ExclusiveArch:  x86_64
# Only needed to regenerate BPF bindings (REGEN_BPF=1):
#BuildRequires:  clang
#BuildRequires:  bpftool
#BuildRequires:  libbpf-devel

# Runtime: setcap is invoked by `funkoverage install` to grant CAP_BPF /
# CAP_PERFMON / CAP_DAC_READ_SEARCH to each shim copy. elfutils is used by
# the DWARF enumeration path for binaries with split debug info.
Requires:       libcap-progs
Requires:       elfutils

# Kernel ≥6.6 is required at runtime for uprobe_multi. We do not enforce
# it via Requires (kernel package versioning varies), but `funkoverage
# setup` validates this at first run.

%description
funkoverage records function-level code coverage of any ELF binary using
the kernel's eBPF uprobe_multi facility. It installs a small Go shim in
place of the target binary; at runtime the shim attaches uprobes against
every enumerated function (via DWARF or .symtab/.dynsym), records the
first call of each via a kernel ringbuf, and writes a CALLED log used
to generate HTML / XML / text coverage reports.

No source code, no recompilation of the target, no Intel Pin.

Requires kernel ≥6.6 (uprobe_multi) and CONFIG_DEBUG_INFO_BTF=y.

%prep
%autosetup -n %{dname} -a1

%build
export GOFLAGS="-buildmode=pie -trimpath -mod=vendor"
mv %{_builddir}/vendor .
go build -ldflags="-s -w" -o funkoverage      ./cmd/
go build -ldflags="-s -w" -o funkoverage-shim ./cmd/shim_binary/

%check
go test ./...

%install
mkdir -p %{buildroot}%{_bindir}
mkdir -p %{buildroot}%{_libdir}/coverage-tools

install -m 0755 funkoverage      %{buildroot}%{_bindir}/funkoverage
install -m 0755 funkoverage-shim %{buildroot}%{_libdir}/coverage-tools/funkoverage-shim

%files
%license LICENSE
%doc README.md
%{_bindir}/funkoverage
%{_libdir}/coverage-tools/

%changelog

# Funkoverage Tracing Overhead Benchmark (#166)

Measures per-call and startup overhead introduced by the `funkoverage` shim and eBPF uprobes.

## Prologue Variants

The Linux kernel uprobe subsystem optimizes function entry by emulating specific first instructions (`nop`, relative `jmp`/`call`, `push reg`) without taking a single-step trap. Instructions outside this set require single-stepping, increasing overhead significantly.

Seven x86_64 variants test different prologues:
- `o0-nofcp`: `-O0 -fcf-protection=none` (`push %rbp` first; emulated)
- `o0-cet`: `-O0 -fcf-protection=full` (`endbr64` first; CET adds non-emulated insn before push)
- `o1-nofcp`: `-O1 -fcf-protection=none` (ordinary insn; single-stepped)
- `o1-cet`: `-O1 -fcf-protection=full` (`endbr64` then ordinary insn; single-stepped)
- `o0-call`: `-O0 -fcf-protection=none -pg -mfentry` (`call <__fentry__>` first; emulated)
- `o1-call`: `-O1 -fcf-protection=none -pg -mfentry` (`call <__fentry__>` first; emulated)
- `o1-cet-call`: `-O1 -fcf-protection=full -pg -mfentry` (`endbr64` then `call <__fentry__>`)

## How to Run

```bash
./build.sh
sudo tests/bench/run.sh            # all of 100/1000/10000
sudo FUNCS="100" RUNS=3 tests/bench/run.sh
sudo tests/bench/run.sh --perf
```

Note: `sudo` resets `PATH`; the script uses absolute paths and does not depend on `PATH` for funkoverage binaries.

## Caveats

- Run manually, not in CI: requires root, Linux kernel ≥ 6.6, and an idle/quiet machine.
- x86_64 only (due to `-fcf-protection` compiler flags and architecture-specific prologues).
- Results depend on virtualization and CPU frequency scaling.
- Startup measurements include libc probes (default install scope; `--no-libs` is not used).
- The `call` variants are built with `-pg`, so glibc's profiling hook runs on every call. Their native baseline is ~30x slower than the others (about 520 ms vs 15 ms for 2M calls). Compare the `ns_per_call` column across variants, not the absolute `*_ms` columns.
- Timed window ends when the traced process exits; the background helper's post-exit log flush is excluded.

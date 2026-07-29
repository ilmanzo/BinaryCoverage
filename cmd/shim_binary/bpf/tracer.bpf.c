// SPDX-License-Identifier: GPL-2.0-only
//
// funkoverage native eBPF tracer.
//
// Replaces the bpftrace-driven tracer. Three pieces:
//
//   1. uprobe.multi program — fires once per traced function entry. Uses
//      bpf_get_attach_cookie() to identify the function (cookie == global
//      func index assigned by user space at attach time).
//
//   2. sched_process_fork tracepoint — propagates the watched-pid set so
//      that tracing follows fork()ed children of the originally-tracked
//      process.
//
//   3. ringbuf events — emitted only on the FIRST observed call per func,
//      bounded total bandwidth = 4 * total_funcs regardless of program
//      runtime. De-dup happens in kernel (atomic CAS on `seen` array).

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>

// Tracepoint argument layout for sched:sched_process_fork on modern kernels
// (≥6.x). comm fields use __data_loc encoding (4-byte offset/length pair into
// the trailing string area) instead of inline 16-byte arrays — see
// /sys/kernel/tracing/events/sched/sched_process_fork/format. We only read
// parent_pid and child_pid, so the comm locations are placeholders.
struct sched_process_fork_args {
    __u64 _common;          // common_type/flags/preempt_count/pid (offset 0..7)
    __u32 _parent_comm_loc; // offset 8..11
    __u32 parent_pid;       // offset 12..15
    __u32 _child_comm_loc;  // offset 16..19
    __u32 child_pid;        // offset 20..23
};

// Ring-buffer event payload. One per *first* call of each traced function.
struct event {
    __u32 func_idx;
};

// Force bpf2go to emit a Go type for `event` via -type event in the directive.
const struct event *unused __attribute__((unused));

// Watched tgids. User space seeds the initial tgid; sched_process_fork
// inherits children. Bounded at 4096 — well above any realistic shim
// invocation's process tree.
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key, __u32);
    __type(value, __u8);
} watched SEC(".maps");

// One u64 per global function index. User space resizes max_entries to
// total_funcs at load time. Value is 0 (unseen) → 1 (seen) via atomic CAS.
// We use u64 because the default BPF CPU (-mcpu=v1) only supports 64-bit
// atomic CAS; the 8x memory cost vs a bitmap is trivial (~800 KB for 100K
// funcs).
struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, __u64);
} seen SEC(".maps");

// 256 KB ring buffer. Each event is 4 bytes and only one event per func
// ever fires (kernel-side dedup), so this comfortably holds 65k unique
// functions of backlog.
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 256 * 1024);
} events SEC(".maps");

SEC("uprobe.multi/probe")
int trace_uprobe(struct pt_regs *ctx)
{
    __u32 tgid = bpf_get_current_pid_tgid() >> 32;
    if (!bpf_map_lookup_elem(&watched, &tgid))
        return 0;

    // The cookie was assigned at attach time as the global func index
    // (see Tracer.Start in tracer.go). Cast narrows uint64→uint32; we
    // never assign indices >= 2^32.
    __u32 idx = (__u32)bpf_get_attach_cookie(ctx);

    __u64 *flag = bpf_map_lookup_elem(&seen, &idx);
    if (!flag)
        return 0;

    // Atomic test-and-set: 0→1 succeeds, 1→1 races lose. Only the winning
    // CPU emits the ringbuf event; subsequent calls (on any CPU) skip.
    if (__sync_val_compare_and_swap(flag, 0ULL, 1ULL) != 0)
        return 0;

    struct event *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
    if (!e)
        return 0;
    e->func_idx = idx;
    bpf_ringbuf_submit(e, 0);
    return 0;
}

// On fork, copy the parent's watched bit to the child. We do not bother
// distinguishing real fork() from clone(CLONE_THREAD): for thread clone,
// child_pid is a TID whose TGID == parent's TGID, which is already in the
// map; the spurious entry is never matched by the uprobe (which keys on
// TGID) and dies with the shim invocation.
SEC("tp/sched/sched_process_fork")
int trace_fork(struct sched_process_fork_args *ctx)
{
    __u32 parent = ctx->parent_pid;
    if (!bpf_map_lookup_elem(&watched, &parent))
        return 0;
    __u32 child = ctx->child_pid;
    __u8 one = 1;
    bpf_map_update_elem(&watched, &child, &one, BPF_ANY);
    return 0;
}

SEC("uretprobe/dlopen")
int trace_dlopen_return(struct pt_regs *ctx)
{
    __u32 tgid = bpf_get_current_pid_tgid() >> 32;
    bpf_printk("dlopen uretprobe fired, tgid: %d\n", tgid);
    if (!bpf_map_lookup_elem(&watched, &tgid))
        return 0;

#if defined(__x86_64__)
    void *handle = (void *)ctx->ax;
#elif defined(__aarch64__)
    void *handle = (void *)ctx->regs[0];
#else
    void *handle = (void *)1; // fallback
#endif

    bpf_printk("dlopen handle: %p\n", handle);
    if (!handle)
        return 0;

    struct event *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
    if (!e)
        return 0;
    e->func_idx = 0xFFFFFFFF; // Reserved token for dlopen
    bpf_ringbuf_submit(e, 0);
    return 0;
}

// No sched_process_exit cleanup. When a non-leader thread calls execve(),
// the kernel's de_thread() kills the old leader, firing sched_process_exit
// with args->pid == TGID — that would prematurely evict our newly-execed
// process from `watched`. The map dies with the shim invocation anyway.

char LICENSE[] SEC("license") = "GPL";

package hw

/*
#include <mach/mach.h>
#include <sys/sysctl.h>

typedef struct {
	uint64_t total, app, wired, compressed, page_size;
	uint64_t swap_total, swap_used;
	int pressure;
	int ok;
} ctx_mem;

static ctx_mem ctx_read_mem(void) {
	ctx_mem m = {0};
	size_t len = sizeof(m.total);
	sysctlbyname("hw.memsize", &m.total, &len, NULL, 0);

	vm_size_t page = 0;
	vm_statistics64_data_t vm;
	mach_msg_type_number_t count = HOST_VM_INFO64_COUNT;
	mach_port_t host = mach_host_self();
	if (host_page_size(host, &page) == KERN_SUCCESS &&
	    host_statistics64(host, HOST_VM_INFO64, (host_info64_t)&vm, &count) == KERN_SUCCESS) {
		m.page_size = page;
		m.app = vm.internal_page_count > vm.purgeable_count
			? vm.internal_page_count - vm.purgeable_count : 0;
		m.wired = vm.wire_count;
		m.compressed = vm.compressor_page_count;
		m.ok = 1;
	}
	mach_port_deallocate(mach_task_self(), host);

	struct xsw_usage swap;
	len = sizeof(swap);
	if (sysctlbyname("vm.swapusage", &swap, &len, NULL, 0) == 0) {
		m.swap_total = swap.xsu_total;
		m.swap_used = swap.xsu_used;
	}

	len = sizeof(m.pressure);
	sysctlbyname("kern.memorystatus_vm_pressure_level", &m.pressure, &len, NULL, 0);
	return m;
}
*/
import "C"

import "github.com/iBvishal/ctxtop/internal/snapshot"

func readMemory(h *snapshot.Host) {
	m := C.ctx_read_mem()
	h.MemTotal = uint64(m.total)
	if m.ok != 0 {
		// "Memory Used" as Activity Monitor counts it: app memory, wired
		// memory and the compressor.
		h.MemUsed = (uint64(m.app) + uint64(m.wired) + uint64(m.compressed)) * uint64(m.page_size)
	}
	h.SwapTotal = uint64(m.swap_total)
	h.SwapUsed = uint64(m.swap_used)
	h.Pressure = pressureName(int(m.pressure))
}

// pressureName maps kern.memorystatus_vm_pressure_level to a name.
func pressureName(level int) string {
	switch level {
	case 1:
		return "normal"
	case 2:
		return "warn"
	case 4:
		return "critical"
	}
	return ""
}

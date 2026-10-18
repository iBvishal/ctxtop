package hw

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation -lIOReport
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>

// IOReport is a private framework without headers. These declarations
// match how macmon and mactop call it.
typedef struct IOReportSubscriptionRef *IOReportSubscriptionRef;
extern CFDictionaryRef IOReportCopyChannelsInGroup(CFStringRef group, CFStringRef subgroup, uint64_t a, uint64_t b, uint64_t c);
extern IOReportSubscriptionRef IOReportCreateSubscription(void *a, CFMutableDictionaryRef channels, CFMutableDictionaryRef *subbed, uint64_t d, CFTypeRef e);
extern CFDictionaryRef IOReportCreateSamples(IOReportSubscriptionRef sub, CFMutableDictionaryRef channels, CFTypeRef a);
extern CFDictionaryRef IOReportCreateSamplesDelta(CFDictionaryRef prev, CFDictionaryRef cur, CFTypeRef a);
extern CFStringRef IOReportChannelGetChannelName(CFDictionaryRef ch);
extern CFStringRef IOReportChannelGetUnitLabel(CFDictionaryRef ch);
extern int64_t IOReportSimpleGetIntegerValue(CFDictionaryRef ch, int32_t idx);

typedef struct {
	double util;     // percent, -1 when unknown
	int64_t mem;     // bytes in use, -1 when unknown
} ctx_gpu;

static int64_t ctx_dict_int(CFDictionaryRef d, const char *key) {
	CFStringRef k = CFStringCreateWithCString(NULL, key, kCFStringEncodingUTF8);
	CFNumberRef n = (CFNumberRef)CFDictionaryGetValue(d, k);
	CFRelease(k);
	int64_t v = -1;
	if (n != NULL && CFGetTypeID(n) == CFNumberGetTypeID()) {
		CFNumberGetValue(n, kCFNumberSInt64Type, &v);
	}
	return v;
}

// ctx_read_gpu reads the Apple GPU driver's PerformanceStatistics, which
// any user may read.
static ctx_gpu ctx_read_gpu(void) {
	ctx_gpu g = {-1, -1};
	io_iterator_t it;
	if (IOServiceGetMatchingServices(kIOMainPortDefault, IOServiceMatching("AGXAccelerator"), &it) != KERN_SUCCESS) {
		return g;
	}
	io_object_t svc;
	while ((svc = IOIteratorNext(it)) != 0) {
		CFTypeRef stats = IORegistryEntryCreateCFProperty(svc, CFSTR("PerformanceStatistics"), kCFAllocatorDefault, 0);
		IOObjectRelease(svc);
		if (stats == NULL) {
			continue;
		}
		if (CFGetTypeID(stats) == CFDictionaryGetTypeID()) {
			int64_t u = ctx_dict_int((CFDictionaryRef)stats, "Device Utilization %");
			int64_t m = ctx_dict_int((CFDictionaryRef)stats, "In use system memory");
			if (u >= 0) g.util = (double)u;
			if (m >= 0) g.mem = m;
		}
		CFRelease(stats);
		if (g.util >= 0) break;
	}
	IOObjectRelease(it);
	return g;
}

// Energy sampling. One subscription lives for the process; each call takes
// a sample and reports energy used since the previous one.
static IOReportSubscriptionRef ctx_sub = NULL;
static CFMutableDictionaryRef ctx_subbed = NULL;
static CFDictionaryRef ctx_prev = NULL;

typedef struct {
	double cpu_j, gpu_j, ane_j;
	int ok;
} ctx_energy;

static double ctx_joules(int64_t v, CFStringRef unit) {
	if (unit == NULL) return 0;
	if (CFStringCompare(unit, CFSTR("mJ"), 0) == kCFCompareEqualTo) return v / 1e3;
	if (CFStringCompare(unit, CFSTR("uJ"), 0) == kCFCompareEqualTo) return v / 1e6;
	if (CFStringCompare(unit, CFSTR("nJ"), 0) == kCFCompareEqualTo) return v / 1e9;
	return 0;
}

static int ctx_has_suffix(CFStringRef s, CFStringRef suffix) {
	return CFStringHasSuffix(s, suffix);
}

// ctx_wanted reports whether a channel is one ctxtop reads: the GPU, the
// CPU totals and the Neural Engine.
static int ctx_wanted(CFStringRef name) {
	if (name == NULL) return 0;
	return CFStringCompare(name, CFSTR("GPU Energy"), 0) == kCFCompareEqualTo ||
		ctx_has_suffix(name, CFSTR("CPU Energy")) ||
		CFStringHasPrefix(name, CFSTR("ANE"));
}

static ctx_energy ctx_read_energy(void) {
	ctx_energy e = {0, 0, 0, 0};
	if (ctx_sub == NULL) {
		CFDictionaryRef ch = IOReportCopyChannelsInGroup(CFSTR("Energy Model"), NULL, 0, 0, 0);
		if (ch == NULL) return e;
		CFMutableDictionaryRef mch = CFDictionaryCreateMutableCopy(kCFAllocatorDefault, CFDictionaryGetCount(ch), ch);
		CFRelease(ch);
		// Subscribe only to the channels read below: the group also has a
		// channel per core and cluster, and sampling them all costs more
		// CPU than the rest of ctxtop together.
		CFArrayRef all = (CFArrayRef)CFDictionaryGetValue(mch, CFSTR("IOReportChannels"));
		if (all != NULL) {
			CFMutableArrayRef keep = CFArrayCreateMutable(kCFAllocatorDefault, 0, &kCFTypeArrayCallBacks);
			for (CFIndex i = 0; i < CFArrayGetCount(all); i++) {
				CFDictionaryRef c = (CFDictionaryRef)CFArrayGetValueAtIndex(all, i);
				if (ctx_wanted(IOReportChannelGetChannelName(c))) {
					CFArrayAppendValue(keep, c);
				}
			}
			CFDictionarySetValue(mch, CFSTR("IOReportChannels"), keep);
			CFRelease(keep);
		}
		ctx_sub = IOReportCreateSubscription(NULL, mch, &ctx_subbed, 0, NULL);
		CFRelease(mch);
		if (ctx_sub == NULL) return e;
	}
	CFDictionaryRef cur = IOReportCreateSamples(ctx_sub, ctx_subbed, NULL);
	if (cur == NULL) return e;
	if (ctx_prev == NULL) {
		ctx_prev = cur;
		return e;
	}
	CFDictionaryRef delta = IOReportCreateSamplesDelta(ctx_prev, cur, NULL);
	CFRelease(ctx_prev);
	ctx_prev = cur;
	if (delta == NULL) return e;

	CFArrayRef chans = (CFArrayRef)CFDictionaryGetValue(delta, CFSTR("IOReportChannels"));
	if (chans != NULL) {
		CFIndex n = CFArrayGetCount(chans);
		for (CFIndex i = 0; i < n; i++) {
			CFDictionaryRef c = (CFDictionaryRef)CFArrayGetValueAtIndex(chans, i);
			CFStringRef name = IOReportChannelGetChannelName(c);
			if (name == NULL) continue;
			double j = ctx_joules(IOReportSimpleGetIntegerValue(c, 0), IOReportChannelGetUnitLabel(c));
			if (CFStringCompare(name, CFSTR("GPU Energy"), 0) == kCFCompareEqualTo) {
				e.gpu_j += j;
			} else if (ctx_has_suffix(name, CFSTR("CPU Energy"))) {
				e.cpu_j += j;
			} else if (CFStringHasPrefix(name, CFSTR("ANE"))) {
				e.ane_j += j;
			}
		}
		e.ok = 1;
	}
	CFRelease(delta);
	return e;
}
*/
import "C"

import (
	"sync"
	"time"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// powerEvery is how often energy is sampled. Each IOReport sample costs a
// few milliseconds of kernel time, more than the rest of a poll, and power
// is an average anyway, so in between the last reading is reused.
const powerEvery = 5 * time.Second

var energy struct {
	sync.Mutex
	last  time.Time
	power *snapshot.Power
}

func readGPU(h *snapshot.Host) {
	g := C.ctx_read_gpu()
	if g.util >= 0 {
		u := float64(g.util)
		h.GPUUtil = &u
	}
	if g.mem >= 0 {
		h.GPUMem = uint64(g.mem)
	}
	h.Power = readPower(time.Now())
}

func readPower(now time.Time) *snapshot.Power {
	energy.Lock()
	defer energy.Unlock()
	if !energy.last.IsZero() && now.Sub(energy.last) < powerEvery && energy.power != nil {
		p := *energy.power
		return &p
	}
	e := C.ctx_read_energy()
	dt := now.Sub(energy.last).Seconds()
	first := energy.last.IsZero()
	energy.last = now
	if e.ok == 0 || first || dt <= 0 {
		return nil
	}
	energy.power = &snapshot.Power{CPU: float64(e.cpu_j) / dt, GPU: float64(e.gpu_j) / dt, ANE: float64(e.ane_j) / dt}
	p := *energy.power
	return &p
}

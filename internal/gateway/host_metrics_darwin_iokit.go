//go:build darwin && cgo

package gateway

/*
#cgo LDFLAGS: -framework CoreFoundation -framework IOKit
#include <CoreFoundation/CoreFoundation.h>
#include <string.h>
#include <ifaddrs.h>
#include <net/if.h>
#include <mach/mach.h>

// Private (but stable since macOS 10.x) IOHIDEventSystem sensor API. It is the
// only way to read °C on Apple Silicon without root: powermetrics needs sudo
// and the legacy SMC keys are gone on recent macOS.
typedef struct __IOHIDEvent *IOHIDEventRef;
typedef struct __IOHIDServiceClient *IOHIDServiceClientRef;
typedef struct __IOHIDEventSystemClient *IOHIDEventSystemClientRef;

extern IOHIDEventSystemClientRef IOHIDEventSystemClientCreate(CFAllocatorRef allocator);
extern int IOHIDEventSystemClientSetMatching(IOHIDEventSystemClientRef client, CFDictionaryRef match);
extern CFArrayRef IOHIDEventSystemClientCopyServices(IOHIDEventSystemClientRef client);
extern CFTypeRef IOHIDServiceClientCopyProperty(IOHIDServiceClientRef service, CFStringRef key);
extern IOHIDEventRef IOHIDServiceClientCopyEvent(IOHIDServiceClientRef service, int64_t type, int32_t options, int64_t timeout);
extern double IOHIDEventGetFloatValue(IOHIDEventRef event, int32_t field);

#define kIOHIDEventTypeTemperature 15
#define kIOHIDEventFieldTemperatureLevel (kIOHIDEventTypeTemperature << 16)

// AppleSensors temperature services.
#define kHIDPage_AppleVendor 0xff00
#define kHIDUsage_AppleVendor_TemperatureSensor 5

typedef struct {
	char   name[64];
	double value;
} gw_temp_sensor_t;

static CFDictionaryRef gw_temp_matching(void) {
	int page = kHIDPage_AppleVendor;
	int usage = kHIDUsage_AppleVendor_TemperatureSensor;
	CFMutableDictionaryRef d = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (!d) return NULL;
	CFNumberRef p = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &page);
	CFNumberRef u = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &usage);
	if (p) { CFDictionarySetValue(d, CFSTR("PrimaryUsagePage"), p); CFRelease(p); }
	if (u) { CFDictionarySetValue(d, CFSTR("PrimaryUsage"), u); CFRelease(u); }
	return d;
}

// gw_read_temp_sensors fills out[] with up to max named °C readings.
// Returns the number written, or -1 when the sensor client is unavailable.
static int gw_read_temp_sensors(gw_temp_sensor_t *out, int max) {
	if (!out || max <= 0) return -1;
	IOHIDEventSystemClientRef client = IOHIDEventSystemClientCreate(kCFAllocatorDefault);
	if (!client) return -1;

	CFDictionaryRef match = gw_temp_matching();
	if (match) {
		IOHIDEventSystemClientSetMatching(client, match);
		CFRelease(match);
	}

	CFArrayRef services = IOHIDEventSystemClientCopyServices(client);
	if (!services) { CFRelease(client); return -1; }

	int n = 0;
	CFIndex count = CFArrayGetCount(services);
	for (CFIndex i = 0; i < count && n < max; i++) {
		IOHIDServiceClientRef svc = (IOHIDServiceClientRef)CFArrayGetValueAtIndex(services, i);
		if (!svc) continue;
		CFStringRef name = (CFStringRef)IOHIDServiceClientCopyProperty(svc, CFSTR("Product"));
		if (!name) continue;
		IOHIDEventRef ev = IOHIDServiceClientCopyEvent(svc, kIOHIDEventTypeTemperature, 0, 0);
		if (ev) {
			double v = IOHIDEventGetFloatValue(ev, kIOHIDEventFieldTemperatureLevel);
			char buf[64];
			buf[0] = '\0';
			if (CFStringGetCString(name, buf, sizeof(buf), kCFStringEncodingUTF8) && buf[0] != '\0') {
				memcpy(out[n].name, buf, sizeof(buf));
				out[n].name[sizeof(out[n].name) - 1] = '\0';
				out[n].value = v;
				n++;
			}
			CFRelease(ev);
		}
		CFRelease(name);
	}

	CFRelease(services);
	CFRelease(client);
	return n;
}

// ---- memory (mach host_statistics64) ----------------------------------

typedef struct {
	unsigned long long page_size;
	unsigned long long total;      // hw.memsize equivalent
	unsigned long long used;       // active + wired + compressed (Activity Monitor style)
} gw_vm_stats_t;

static int gw_read_vm_stats(gw_vm_stats_t *out) {
	if (!out) return -1;
	mach_port_t host = mach_host_self();
	vm_size_t page_size = 0;
	if (host_page_size(host, &page_size) != KERN_SUCCESS || page_size == 0) return -1;

	vm_statistics64_data_t vm;
	mach_msg_type_number_t count = HOST_VM_INFO64_COUNT;
	if (host_statistics64(host, HOST_VM_INFO64, (host_info64_t)&vm, &count) != KERN_SUCCESS) return -1;

	unsigned long long ps = (unsigned long long)page_size;
	unsigned long long used_pages =
		(unsigned long long)vm.active_count +
		(unsigned long long)vm.wire_count +
		(unsigned long long)vm.compressor_page_count;
	unsigned long long all_pages = used_pages +
		(unsigned long long)vm.free_count +
		(unsigned long long)vm.inactive_count +
		(unsigned long long)vm.speculative_count;

	out->page_size = ps;
	out->total = all_pages * ps;
	out->used = used_pages * ps;
	return 0;
}

// ---- network counters (getifaddrs / AF_LINK) --------------------------

typedef struct {
	char               name[32];
	unsigned long long rx;
	unsigned long long tx;
} gw_if_counter_t;

static int gw_read_if_counters(gw_if_counter_t *out, int max) {
	if (!out || max <= 0) return -1;
	struct ifaddrs *head = NULL;
	if (getifaddrs(&head) != 0 || !head) return -1;
	int n = 0;
	for (struct ifaddrs *p = head; p != NULL && n < max; p = p->ifa_next) {
		if (!p->ifa_addr || p->ifa_addr->sa_family != AF_LINK || !p->ifa_data) continue;
		struct if_data *d = (struct if_data *)p->ifa_data;
		strncpy(out[n].name, p->ifa_name ? p->ifa_name : "", sizeof(out[n].name) - 1);
		out[n].name[sizeof(out[n].name) - 1] = '\0';
		out[n].rx = (unsigned long long)d->ifi_ibytes;
		out[n].tx = (unsigned long long)d->ifi_obytes;
		n++;
	}
	freeifaddrs(head);
	return n;
}
*/
import "C"

const darwinMaxTempSensors = 128

// darwinReadTempSensors returns every AppleSensors temperature reading.
// Works for the current user without sudo on both Apple Silicon and Intel.
func darwinReadTempSensors() []tempSensorReading {
	buf := make([]C.gw_temp_sensor_t, darwinMaxTempSensors)
	n := int(C.gw_read_temp_sensors(&buf[0], C.int(darwinMaxTempSensors)))
	if n <= 0 {
		return nil
	}
	out := make([]tempSensorReading, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, tempSensorReading{
			Name:  C.GoString(&buf[i].name[0]),
			Value: float64(buf[i].value),
		})
	}
	return out
}

// darwinReadMemory returns total/used physical memory in bytes.
func darwinReadMemory() (total, used uint64, ok bool) {
	var st C.gw_vm_stats_t
	if C.gw_read_vm_stats(&st) != 0 {
		return 0, 0, false
	}
	return uint64(st.total), uint64(st.used), uint64(st.total) > 0
}

const darwinMaxInterfaces = 64

// darwinReadInterfaceCounters returns per-interface cumulative rx/tx bytes.
func darwinReadInterfaceCounters() []interfaceCounter {
	buf := make([]C.gw_if_counter_t, darwinMaxInterfaces)
	n := int(C.gw_read_if_counters(&buf[0], C.int(darwinMaxInterfaces)))
	if n <= 0 {
		return nil
	}
	out := make([]interfaceCounter, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, interfaceCounter{
			Name: C.GoString(&buf[i].name[0]),
			RX:   uint64(buf[i].rx),
			TX:   uint64(buf[i].tx),
		})
	}
	return out
}

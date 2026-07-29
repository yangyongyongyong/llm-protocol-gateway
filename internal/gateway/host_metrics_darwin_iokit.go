//go:build darwin && cgo

package gateway

/*
#cgo LDFLAGS: -framework CoreFoundation -framework IOKit
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>
#include <string.h>
#include <stdio.h>
#include <stdlib.h>
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

// ---- AppleSMC fan RPM (80-byte SMCKeyData protocol) --------------------

typedef struct { uint8_t major, minor, build, reserved; uint16_t release; } gw_smc_version_t;
typedef struct { uint16_t version, length; uint32_t cpu_plimit, gpu_plimit, mem_plimit; } gw_smc_plimit_t;
typedef struct { uint32_t dataSize; uint32_t dataType; uint8_t dataAttributes; } gw_smc_keyinfo_t;
typedef struct {
	uint32_t key;
	gw_smc_version_t vers;
	gw_smc_plimit_t pLimitData;
	gw_smc_keyinfo_t keyInfo;
	uint8_t result;
	uint8_t status;
	uint8_t data8;
	uint32_t data32;
	uint8_t bytes[32];
} gw_smc_keydata_t;

enum {
	gw_kSMCHandleYPCEvent = 2,
	gw_kSMCReadKey = 5,
	gw_kSMCGetKeyInfo = 9
};

typedef struct {
	int    id;
	char   name[32];
	double rpm;
	double min_rpm;
	double max_rpm;
} gw_fan_t;

static uint32_t gw_smc_fourcc(const char *s) {
	return ((uint32_t)(unsigned char)s[0] << 24) |
		((uint32_t)(unsigned char)s[1] << 16) |
		((uint32_t)(unsigned char)s[2] << 8) |
		(uint32_t)(unsigned char)s[3];
}

static int gw_smc_call(io_connect_t conn, uint32_t selector, gw_smc_keydata_t *in, gw_smc_keydata_t *out) {
	size_t outsz = sizeof(*out);
	return IOConnectCallStructMethod(conn, selector, in, sizeof(*in), out, &outsz) == KERN_SUCCESS;
}

static int gw_smc_read_key(io_connect_t conn, const char *key, uint8_t *buf, uint32_t *size, uint32_t *type) {
	gw_smc_keydata_t in, out;
	memset(&in, 0, sizeof(in));
	memset(&out, 0, sizeof(out));
	in.key = gw_smc_fourcc(key);
	in.data8 = gw_kSMCGetKeyInfo;
	if (!gw_smc_call(conn, gw_kSMCHandleYPCEvent, &in, &out) || out.result != 0) return 0;
	*size = out.keyInfo.dataSize;
	*type = out.keyInfo.dataType;
	memset(&in, 0, sizeof(in));
	memset(&out, 0, sizeof(out));
	in.key = gw_smc_fourcc(key);
	in.keyInfo.dataSize = *size;
	in.data8 = gw_kSMCReadKey;
	if (!gw_smc_call(conn, gw_kSMCHandleYPCEvent, &in, &out) || out.result != 0) return 0;
	if (*size > 32) *size = 32;
	memcpy(buf, out.bytes, *size);
	return 1;
}

static int gw_smc_read_float(io_connect_t conn, const char *key, double *out) {
	uint8_t buf[32];
	uint32_t size = 0, type = 0;
	if (!gw_smc_read_key(conn, key, buf, &size, &type) || size == 0) return 0;
	if (type == gw_smc_fourcc("flt ") && size >= 4) {
		float f;
		memcpy(&f, buf, 4);
		*out = (double)f;
		return 1;
	}
	if (type == gw_smc_fourcc("fpe2") && size >= 2) {
		*out = (double)(((uint16_t)buf[0] << 8) | buf[1]) / 4.0;
		return 1;
	}
	if (type == gw_smc_fourcc("ui8 ") && size >= 1) {
		*out = (double)buf[0];
		return 1;
	}
	if (type == gw_smc_fourcc("ui16") && size >= 2) {
		*out = (double)(((uint16_t)buf[0] << 8) | buf[1]);
		return 1;
	}
	return 0;
}

static int gw_smc_read_u8(io_connect_t conn, const char *key, int *out) {
	double v = 0;
	if (!gw_smc_read_float(conn, key, &v)) return 0;
	*out = (int)v;
	return 1;
}

// gw_read_fans fills out[] with up to max fans from AppleSMC. Returns count or -1.
static int gw_read_fans(gw_fan_t *out, int max) {
	if (!out || max <= 0) return -1;
	io_service_t svc = IOServiceGetMatchingService(kIOMainPortDefault, IOServiceMatching("AppleSMC"));
	if (!svc) return -1;
	io_connect_t conn = 0;
	kern_return_t kr = IOServiceOpen(svc, mach_task_self(), 0, &conn);
	IOObjectRelease(svc);
	if (kr != KERN_SUCCESS || !conn) return -1;

	int nFans = 0;
	if (!gw_smc_read_u8(conn, "FNum", &nFans) || nFans <= 0) {
		IOServiceClose(conn);
		return 0;
	}
	if (nFans > max) nFans = max;

	int n = 0;
	for (int i = 0; i < nFans; i++) {
		char kAc[5], kMn[5], kMx[5];
		snprintf(kAc, sizeof(kAc), "F%dAc", i);
		snprintf(kMn, sizeof(kMn), "F%dMn", i);
		snprintf(kMx, sizeof(kMx), "F%dMx", i);
		double rpm = 0, minv = 0, maxv = 0;
		if (!gw_smc_read_float(conn, kAc, &rpm)) continue;
		(void)gw_smc_read_float(conn, kMn, &minv);
		(void)gw_smc_read_float(conn, kMx, &maxv);
		out[n].id = i;
		snprintf(out[n].name, sizeof(out[n].name), "Fan %d", i);
		out[n].rpm = rpm;
		out[n].min_rpm = minv;
		out[n].max_rpm = maxv;
		n++;
	}
	IOServiceClose(conn);
	return n;
}
*/
import "C"
import "math"

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

const darwinMaxFans = 8

// darwinReadFans returns chassis fan RPM readings via AppleSMC (no root required for reads).
func darwinReadFans() []HostFanSpeed {
	buf := make([]C.gw_fan_t, darwinMaxFans)
	n := int(C.gw_read_fans(&buf[0], C.int(darwinMaxFans)))
	if n <= 0 {
		return nil
	}
	out := make([]HostFanSpeed, 0, n)
	for i := 0; i < n; i++ {
		rpm := float64(buf[i].rpm)
		minRPM := float64(buf[i].min_rpm)
		maxRPM := float64(buf[i].max_rpm)
		name := C.GoString(&buf[i].name[0])
		if n == 1 {
			name = "系统风扇"
		}
		out = append(out, HostFanSpeed{
			ID:      int(buf[i].id),
			Name:    name,
			RPM:     math.Round(rpm),
			MinRPM:  math.Round(minRPM),
			MaxRPM:  math.Round(maxRPM),
			Percent: roundTemp(fanSpeedPercent(rpm, minRPM, maxRPM)),
			Source:  "smc",
		})
	}
	return out
}

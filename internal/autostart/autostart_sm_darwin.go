//go:build darwin && cgo

package autostart

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework ServiceManagement
#include <stdlib.h>
#import <Foundation/Foundation.h>
#import <ServiceManagement/ServiceManagement.h>

static long llmGatewayLegacyStatus(const char *path) {
	if (@available(macOS 13.0, *)) {
		@autoreleasepool {
			NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
			return (long)[SMAppService statusForLegacyURL:url];
		}
	}
	return -1;
}
*/
import "C"

import "unsafe"

func init() {
	legacyLaunchAgentStatus = func(path string) int {
		p := C.CString(path)
		defer C.free(unsafe.Pointer(p))
		return int(C.llmGatewayLegacyStatus(p))
	}
}

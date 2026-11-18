//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <AVFoundation/AVFoundation.h>
#import <CoreBluetooth/CoreBluetooth.h>
#import <UserNotifications/UserNotifications.h>
#import <ApplicationServices/ApplicationServices.h>
#import <Security/Security.h>
#include <stdint.h>
#include <string.h>

extern int goPermissionActive(uint64_t token);
extern void goPermissionDone(uint64_t token, int state, int busy, int request, char *detail, char *failure);

enum { Unknown, NotDetermined, Restricted, Denied, Granted, NotGranted, Limited, Manual, Unavailable, NotApplicable };
// Same order as ids in permissions.go.
enum { Bluetooth, Notifications, FullDisk, Microphone, Camera, Accessibility, Screen, SystemAudio, Input, Automation, Count };
// Accessed only on the main queue, including asynchronous completion handlers.
static BOOL pending[Count];
static BOOL requested[Count];
static CBCentralManager *central;

@interface YMGrantBluetoothDelegate : NSObject <CBCentralManagerDelegate>
@end
@implementation YMGrantBluetoothDelegate
- (void)centralManagerDidUpdateState:(CBCentralManager *)manager { /* Never scan or connect. */ }
@end
static YMGrantBluetoothDelegate *bluetoothDelegate;

static void finish(uint64_t token, int state, BOOL busy, BOOL request, NSString *detail, NSString *failure) {
    goPermissionDone(token, state, busy, request, (char *)[detail UTF8String], (char *)[failure UTF8String]);
}
static BOOL bundled(void) {
    NSBundle *bundle = NSBundle.mainBundle;
    return [bundle.bundlePath.pathExtension isEqualToString:@"app"] &&
        [bundle.bundleIdentifier isEqualToString:@"dev.idy.yesman"];
}
static int tcc(NSInteger value) {
    switch (value) {
        case 0: return NotDetermined;
        case 1: return Restricted;
        case 2: return Denied;
        case 3: return Granted;
        default: return Unknown;
    }
}
static NSString *usage(int permission) {
    switch (permission) {
        case Bluetooth: return @"NSBluetoothAlwaysUsageDescription";
        case Microphone: return @"NSMicrophoneUsageDescription";
        case Camera: return @"NSCameraUsageDescription";
        default: return nil;
    }
}
static BOOL hasUsage(int permission) {
    NSString *key = usage(permission);
    if (!key) return YES;
    id value = [NSBundle.mainBundle objectForInfoDictionaryKey:key];
    return [value isKindOfClass:NSString.class] && [value length] > 0;
}
static int status(int permission) {
    switch (permission) {
        case Bluetooth: return tcc(CBManager.authorization);
        case Microphone: return tcc([AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio]);
        case Camera: return tcc([AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeVideo]);
        // These APIs return a Boolean, not a TCC decision history.
        case Accessibility: return AXIsProcessTrusted() ? Granted : NotGranted;
        case Screen: return CGPreflightScreenCaptureAccess() ? Granted : NotGranted;
        case Input: return CGPreflightListenEventAccess() ? Granted : NotGranted;
        default: return Unknown;
    }
}
static BOOL manualOnly(int permission) {
    return permission == FullDisk || permission == SystemAudio || permission == Automation;
}
static void query(uint64_t token, int permission) {
    if (permission == SystemAudio) {
        // Core Audio process taps (macOS 14.2+) have no public status or
        // request call: macOS asks when a tap first records. Never create one
        // to probe. Screen Recording's preflight is a different permission.
        if (@available(macOS 14.2, *)) finish(token, Manual, NO, NO, @"system-audio-manual", nil);
        else finish(token, NotApplicable, NO, NO, @"system-audio-os", nil);
        return;
    }
    if (permission == FullDisk || permission == Automation) {
        finish(token, Manual, NO, NO, permission == FullDisk ? @"full-disk-manual" : @"automation-manual", nil);
        return;
    }
    if (!bundled()) {
        finish(token, Unavailable, NO, NO, @"bundle-required", nil);
        return;
    }
    if (permission == Notifications) {
        // Never replace MyGo's notification delegate. Its delivery path uses
        // this same center, requesting alert, sound and badge authorization.
        [UNUserNotificationCenter.currentNotificationCenter getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *settings) {
            dispatch_async(dispatch_get_main_queue(), ^{
                if (!settings) { finish(token, Unknown, pending[permission], NO, nil, @"The system returned no notification settings."); return; }
                int state = Unknown;
                NSString *detail = nil;
                switch (settings.authorizationStatus) {
                    case UNAuthorizationStatusNotDetermined: state = NotDetermined; break;
                    case UNAuthorizationStatusDenied: state = Denied; break;
                    case UNAuthorizationStatusAuthorized:
                        state = Granted;
                        if (settings.alertSetting != UNNotificationSettingEnabled || settings.alertStyle == UNAlertStyleNone) {
                            state = Limited; detail = @"notification-alerts-off";
                        } else if (settings.soundSetting != UNNotificationSettingEnabled) {
                            detail = @"notification-sound-off";
                        }
                        break;
                    case UNAuthorizationStatusProvisional: state = Limited; detail = @"notification-provisional"; break;
                    default: break;
                }
                finish(token, state, pending[permission], state == NotDetermined, detail, nil);
            });
        }];
        return;
    }
    int state = status(permission);
    BOOL request = (state == NotDetermined || (state == NotGranted && !requested[permission])) && hasUsage(permission);
    NSString *detail = (permission == Screen || permission == Input) ? @"restart-may-be-needed" : nil;
    if (state == NotGranted && permission == Accessibility) detail = @"boolean-status";
    if (!hasUsage(permission)) detail = @"usage-missing";
    finish(token, state, pending[permission], request, detail, nil);
}
static void completeRequest(uint64_t token, int permission, NSString *failure) {
    pending[permission] = NO;
    finish(token, Unknown, NO, NO, nil, failure);
}
static void pollBluetooth(uint64_t token, int remaining) {
    @try {
        if (status(Bluetooth) != NotDetermined) { completeRequest(token, Bluetooth, nil); return; }
        if (remaining <= 0) { completeRequest(token, Bluetooth, @"Bluetooth authorization is still undetermined. Check System Settings, then refresh."); return; }
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, NSEC_PER_SEC / 2), dispatch_get_main_queue(), ^{ pollBluetooth(token, remaining - 1); });
    } @catch (NSException *exception) { completeRequest(token, Bluetooth, exception.reason); }
}
static void request(uint64_t token, int permission) {
    if (!bundled()) { finish(token, Unavailable, NO, NO, nil, @"Open the packaged yesman app to request its permissions."); return; }
    if (manualOnly(permission)) { finish(token, Manual, NO, NO, nil, @"Manage this permission in System Settings."); return; }
    for (int i = 0; i < Count; i++) {
        if (pending[i]) { finish(token, Unknown, YES, NO, nil, @"A system permission request is still pending."); return; }
    }
    if (!hasUsage(permission)) { finish(token, Unavailable, NO, NO, nil, @"This build is missing its permission usage description."); return; }
    pending[permission] = YES;
    requested[permission] = YES;
    switch (permission) {
        case Notifications:
            [UNUserNotificationCenter.currentNotificationCenter requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound | UNAuthorizationOptionBadge) completionHandler:^(BOOL granted, NSError *error) {
                dispatch_async(dispatch_get_main_queue(), ^{ completeRequest(token, permission, error.localizedDescription); });
            }];
            return;
        case Camera:
        case Microphone:
            [AVCaptureDevice requestAccessForMediaType:permission == Camera ? AVMediaTypeVideo : AVMediaTypeAudio completionHandler:^(BOOL granted) {
                dispatch_async(dispatch_get_main_queue(), ^{ completeRequest(token, permission, nil); });
            }];
            return;
        case Bluetooth:
            if (!central) {
                bluetoothDelegate = [YMGrantBluetoothDelegate new];
                central = [[CBCentralManager alloc] initWithDelegate:bluetoothDelegate queue:dispatch_get_main_queue() options:@{CBCentralManagerOptionShowPowerAlertKey:@NO}];
            }
            pollBluetooth(token, 240);
            return;
        case Accessibility:
            AXIsProcessTrustedWithOptions((CFDictionaryRef)@{(NSString *)kAXTrustedCheckOptionPrompt:@YES});
            break;
        case Screen:
        case Input:
            // CoreGraphics has synchronous request APIs. Keep any system wait
            // off MyGo's UI thread; pending remains owned by the main queue.
            dispatch_async(dispatch_get_global_queue(QOS_CLASS_USER_INITIATED, 0), ^{
                @autoreleasepool {
                    NSString *failure = nil;
                    @try {
                        if (goPermissionActive(token)) {
                            if (permission == Screen) CGRequestScreenCaptureAccess();
                            else CGRequestListenEventAccess();
                        }
                    } @catch (NSException *exception) { failure = exception.reason ?: @"The system permission API failed."; }
                    dispatch_async(dispatch_get_main_queue(), ^{ completeRequest(token, permission, failure); });
                }
            });
            return;
        default: break;
    }
    completeRequest(token, permission, nil);
}

// Reads this process's own code signature; it does not contact TCC.
// 0 unknown, 1 unsigned, 2 ad-hoc, 3 certificate signed.
int ym_permission_signing(void) {
    int signing = 0;
    @autoreleasepool {
        SecCodeRef code = NULL;
        SecStaticCodeRef disk = NULL;
        CFDictionaryRef info = NULL;
        OSStatus status = SecCodeCopySelf(kSecCSDefaultFlags, &code);
        if (status == errSecSuccess) status = SecCodeCopyStaticCode(code, kSecCSDefaultFlags, &disk);
        if (status == errSecSuccess) status = SecCodeCopySigningInformation(disk, kSecCSSigningInformation, &info);
        if (status == errSecCSUnsigned) signing = 1;
        else if (status == errSecSuccess && info) {
            NSDictionary *values = (NSDictionary *)info;
            NSNumber *flags = values[(NSString *)kSecCodeInfoFlags];
            if (!values[(NSString *)kSecCodeInfoUnique]) signing = 1;
            else if ([flags isKindOfClass:NSNumber.class]) signing = (flags.unsignedIntValue & kSecCodeSignatureAdhoc) ? 2 : 3;
        }
        if (info) CFRelease(info);
        if (disk) CFRelease(disk);
        if (code) CFRelease(code);
    }
    return signing;
}
// The packaged app's path (caller frees), or NULL for any other copy.
char *ym_permission_bundle(void) {
    @autoreleasepool {
        return bundled() ? strdup(NSBundle.mainBundle.bundlePath.fileSystemRepresentation) : NULL;
    }
}

void ym_permission_start(uint64_t token, int permission, int action, const char *url) {
    // Copy before returning to Go, which owns and frees the input string.
    NSString *destination = [[NSString alloc] initWithUTF8String:url];
    dispatch_async(dispatch_get_main_queue(), ^{
        @autoreleasepool {
            if (!goPermissionActive(token)) return;
            if (permission < 0 || permission >= Count) { finish(token, Unknown, NO, NO, nil, @"Unknown permission."); return; }
            @try {
                switch (action) {
                    case 0: query(token, permission); break;
                    case 1: request(token, permission); break;
                    case 2: {
                        [NSWorkspace.sharedWorkspace openURL:[NSURL URLWithString:destination] configuration:NSWorkspaceOpenConfiguration.configuration completionHandler:^(NSRunningApplication *app, NSError *error) {
                            NSString *failure = error.localizedDescription;
                            if (!app && !failure) failure = @"System Settings could not be opened. Open it manually and choose Privacy & Security or Notifications.";
                            finish(token, Unknown, NO, NO, nil, failure);
                        }];
                        break;
                    }
                    default: finish(token, Unknown, NO, NO, nil, @"Unknown permission action."); break;
                }
            } @catch (NSException *exception) {
                if (action == 1) pending[permission] = NO;
                finish(token, Unknown, NO, NO, nil, exception.reason ?: @"The system permission API failed.");
            }
        }
    });
    [destination release]; // dispatch's copied block owns its captured string
}

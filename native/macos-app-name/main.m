#import <Cocoa/Cocoa.h>
#include <stdio.h>

@interface AppNameProbe : NSObject <NSApplicationDelegate>
@end

@implementation AppNameProbe
- (void)applicationDidFinishLaunching:(NSNotification *)notification
{
    (void)notification;
    [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
    puts("READY");
    fflush(stdout);
}
@end

int main(int argc, const char *argv[])
{
    @autoreleasepool {
        if (argc != 2) return 2;
        [[NSProcessInfo processInfo] setProcessName:[NSString stringWithUTF8String:argv[1]]];

        [NSApplication sharedApplication];
        AppNameProbe *delegate = [[[AppNameProbe alloc] init] autorelease];
        NSApp.delegate = delegate;
        [NSApp run];
    }
    return 0;
}

#import "virtualization_view.h"
#import <objc/runtime.h>

static NSURL *openedURL;

@interface NSWorkspace (MenuProbe)
- (BOOL)captureOpenURL:(NSURL *)url;
@end

@implementation NSWorkspace (MenuProbe)
- (BOOL)captureOpenURL:(NSURL *)url
{
    [openedURL release];
    openedURL = [url copy];
    return YES;
}
@end

@interface AppDelegate (MenuProbe)
- (void)setupMenuBar;
@end

static void require(BOOL condition, NSString *message)
{
    if (!condition) {
        NSLog(@"FAIL: %@", message);
        exit(1);
    }
}

static void collectLabels(NSView *view, NSMutableArray *labels)
{
    if ([view isKindOfClass:[NSTextField class]]) {
        [labels addObject:view];
    }
    for (NSView *child in view.subviews) collectLabels(child, labels);
}

int main(int argc, const char *argv[])
{
    @autoreleasepool {
        if (argc != 1 && argc != 2) return 2;
        BOOL branded = argc == 2;
        NSString *instanceName = branded ? @"Voom: menu-probe" : @"vfkit";
        [[NSProcessInfo processInfo] setProcessName:instanceName];
        [NSApplication sharedApplication];
        AppDelegate *delegate = [[[AppDelegate alloc] init] autorelease];
        NSApp.delegate = delegate;
        [delegate setupMenuBar];

        method_exchangeImplementations(class_getInstanceMethod([NSWorkspace class], @selector(openURL:)),
            class_getInstanceMethod([NSWorkspace class], @selector(captureOpenURL:)));

        NSMenu *help = NSApp.helpMenu;
        if (branded) {
            for (NSMenuItem *item in help.itemArray) {
                require(item.action != NSSelectorFromString(@"reportIssue:"), @"Report Issue should not appear in Voom's Help menu");
            }
        } else {
            NSMenuItem *report = [help itemWithTitle:@"Report issue"];
            require(report != nil, @"Upstream Report Issue menu item is missing");
            require([NSApp sendAction:report.action to:report.target from:report], @"Report Issue action failed");
            require([openedURL.absoluteString isEqualToString:@"https://github.com/Code-Hex/vz/issues/new"], @"Report Issue opened the wrong tracker");
        }

        NSMenuItem *documentation = [help itemWithTitle:@"Voom Help"];
        require((documentation != nil) == branded, @"Voom Help should only appear in branded apps");
        if (branded) {
            require([NSApp sendAction:documentation.action to:documentation.target from:documentation], @"Voom Help action failed");
            require([openedURL.absoluteString isEqualToString:@"https://github.com/mjrusso/voom/blob/main/docs/macos-guests.md"], @"Voom Help opened the wrong documentation");
        }

        NSMenu *app = [NSApp.mainMenu itemAtIndex:0].submenu;
        require([app itemWithTitle:branded ? @"About Voom" : @"About vfkit"] != nil, @"About menu has the wrong name");
        NSMenuItem *quit = [app itemWithTitle:[@"Quit " stringByAppendingString:instanceName]];
        require(quit.action == @selector(terminate:) && [quit.keyEquivalent isEqualToString:@"q"], @"Quit behavior changed");
        require([NSApp.windowsMenu itemWithTitle:@"Minimize"] != nil, @"Window menu is missing");
        require([app itemWithTitle:@"Enable to send system hot keys to virtual machine"] != nil, @"Keyboard capture control is missing");

        AboutViewController *about = [[[AboutViewController alloc] init] autorelease];
        NSMutableArray *labels = [NSMutableArray array];
        collectLabels(about.view, labels);
        NSArray *text = [labels valueForKey:@"stringValue"];
        require([text containsObject:branded ? @"Voom" : instanceName], @"About has the wrong product name");
        if (branded) {
            NSString *version = [NSString stringWithFormat:@"Version %s", argv[1]];
            require([text containsObject:version], @"About has the wrong Voom version");
            require([text containsObject:instanceName], @"About is missing the VM name");
            NSTextField *title = labels[[text indexOfObject:@"Voom"]];
            id projectLink = [title.attributedStringValue attribute:NSLinkAttributeName atIndex:0 effectiveRange:NULL];
            require([[projectLink description] isEqualToString:@"https://github.com/mjrusso/voom"], @"Voom title has the wrong project link");
            require([[text componentsJoinedByString:@"\n"] containsString:@"voom gui menu-probe"], @"About is missing the command to reopen this VM");
            NSUInteger licensesIndex = [text indexOfObject:@"Third-party licenses"];
            require(licensesIndex != NSNotFound, @"About is missing the licenses link");
            NSTextField *licenses = labels[licensesIndex];
            id licenseLink = [licenses.attributedStringValue attribute:NSLinkAttributeName atIndex:0 effectiveRange:NULL];
            require([[licenseLink description] isEqualToString:@"https://github.com/mjrusso/voom/blob/main/docs/third-party-licenses.md"], @"Third-party licenses has the wrong link");
            NSTextField *creditLabel = nil;
            for (NSTextField *label in labels) {
                if ([label.stringValue containsString:@"gvproxy"]) creditLabel = label;
            }
            require(creditLabel != nil, @"About is missing dependency credits");
            NSDictionary *links = @{
                @"vfkit": @"https://github.com/crc-org/vfkit",
                @"vz": @"https://github.com/Code-Hex/vz",
                @"gvproxy": @"https://github.com/containers/gvisor-tap-vsock",
                @"Apple’s Virtualization.framework": @"https://developer.apple.com/documentation/virtualization"
            };
            for (NSString *name in links) {
                NSUInteger location = [creditLabel.stringValue rangeOfString:name].location;
                require(location != NSNotFound, @"About is missing a dependency credit");
                id link = [creditLabel.attributedStringValue attribute:NSLinkAttributeName atIndex:location effectiveRange:NULL];
                require([[link description] isEqualToString:links[name]], @"Dependency credit has the wrong link");
            }
        } else {
            require([text containsObject:@"Powered by github.com/Code-Hex/vz"], @"Upstream About credits changed");
        }
        NSLog(@"PASS: %@ menus, links, and About", branded ? @"Voom" : @"upstream");
    }
    return 0;
}

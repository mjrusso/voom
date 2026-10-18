#include <stdio.h>
#include <string.h>
#import <Foundation/Foundation.h>
#import <Virtualization/Virtualization.h>

int main(int argc, const char *argv[]) {
    @autoreleasepool {
        BOOL selfTest = argc == 2 && strcmp(argv[1], "--self-test") == 0;
        if (argc != 2) {
            fprintf(stderr, "usage: voom-macos-identity MachineIdentifier | --self-test\n");
            return 2;
        }
        NSError *error = nil;
        NSURL *url = selfTest ? nil : [NSURL fileURLWithPath:@(argv[1])];
        NSData *originalData = selfTest ? nil : [NSData dataWithContentsOfURL:url options:0 error:&error];
        VZMacMachineIdentifier *original = selfTest ? [[VZMacMachineIdentifier alloc] init] :
            (originalData ? [[VZMacMachineIdentifier alloc] initWithDataRepresentation:originalData] : nil);
        if (!original) {
            fprintf(stderr, "invalid source machine identifier: %s\n", error ? error.localizedDescription.UTF8String : "cannot decode identifier");
            return 1;
        }
        VZMacMachineIdentifier *fresh = [[VZMacMachineIdentifier alloc] init];
        NSData *data = fresh.dataRepresentation;
        VZMacMachineIdentifier *decoded = [[VZMacMachineIdentifier alloc] initWithDataRepresentation:data];
        if ([fresh isEqual:original] || ![decoded isEqual:fresh]) {
            fprintf(stderr, "fresh machine identifier failed validation\n");
            return 1;
        }
        if (!selfTest) {
            if (![data writeToURL:url options:NSDataWritingAtomic error:&error]) {
                fprintf(stderr, "%s\n", error.localizedDescription.UTF8String);
                return 1;
            }
            NSData *saved = [NSData dataWithContentsOfURL:url options:0 error:&error];
            if (![saved isEqualToData:data]) {
                fprintf(stderr, "machine identifier read-back failed\n");
                return 1;
            }
        }
        return 0;
    }
}

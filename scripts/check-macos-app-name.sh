#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 2 ]]; then
  echo "usage: $0 <vfkit-pid> <vm-name>" >&2
  exit 2
fi

if [[ "$(uname -s)" != Darwin ]]; then
  echo "Checking the registered app name requires a logged-in MacOS session." >&2
  exit 2
fi

if [[ ! "$1" =~ ^[1-9][0-9]*$ ]]; then
  echo "vfkit-pid must be a positive integer" >&2
  exit 2
fi

/usr/bin/osascript -l JavaScript - "$1" "$2" <<'JXA'
ObjC.import('AppKit');

function run(argv) {
    const pid = Number(argv[0]);
    const expected = 'Voom: ' + argv[1];
    const applications = $.NSWorkspace.sharedWorkspace.runningApplications;
    for (let i = 0; i < applications.count; i++) {
        const application = applications.objectAtIndex(i);
        if (Number(application.processIdentifier) !== pid) continue;
        const actual = ObjC.unwrap(application.localizedName);
        if (actual !== expected) {
            throw new Error('FAIL: PID ' + pid + ' has registered app name ' +
                JSON.stringify(actual) + '; expected ' + JSON.stringify(expected));
        }
        const path = ObjC.unwrap(application.bundleURL.path);
        const filesystem = ObjC.unwrap($.NSFileManager.defaultManager.displayNameAtPath(path));
        if (filesystem !== expected && filesystem !== expected + '.app') {
            throw new Error('FAIL: PID ' + pid + ' has filesystem display name ' +
                JSON.stringify(filesystem) + '; expected ' + JSON.stringify(expected) +
                ' with an optional .app extension');
        }
        return 'PASS: PID ' + pid + ' has registered app name ' + JSON.stringify(actual) +
            ' and filesystem display name ' + JSON.stringify(filesystem);
    }
    throw new Error('No running MacOS application found for PID ' + pid);
}
JXA

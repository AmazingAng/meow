#!/usr/bin/env python3
"""Inspect a real Meow archive or exported IPA; an archive is not a TestFlight build."""
import argparse
import hashlib
import json
from pathlib import Path
import plistlib
import subprocess
import tempfile
import wave
import zipfile


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def command(*args):
    return subprocess.check_output(args, stderr=subprocess.PIPE)


def verify(app, distribution, version, build):
    extensions = list((app / "PlugIns").glob("*.appex"))
    require(len(extensions) == 1, "expected exactly one notification extension")
    result = []
    for bundle, identifier in [(app, "blue.meow.app"), (extensions[0], "blue.meow.app.NotificationService")]:
        info = plistlib.loads((bundle / "Info.plist").read_bytes())
        require(info["CFBundleIdentifier"] == identifier, f"wrong bundle identifier: {bundle}")
        require(info["CFBundleShortVersionString"] == version and info["CFBundleVersion"] == build,
                f"wrong version/build: {bundle}")
        require(info["UIDeviceFamily"] == [1], f"expected iPhone target: {bundle}")
        sound = bundle / "meow.wav"
        require(sound.is_file(), f"missing bundled notification sound: {bundle}")
        with wave.open(str(sound)) as audio:
            duration = audio.getnframes() / audio.getframerate()
            require(0 < duration < 30 and audio.getcomptype() == "NONE" and audio.getsampwidth() == 2,
                    f"notification sound must be short 16-bit PCM: {bundle}")
        manifest = plistlib.loads((bundle / "PrivacyInfo.xcprivacy").read_bytes())
        require(manifest.get("NSPrivacyTracking") is False and manifest.get("NSPrivacyTrackingDomains") == [],
                f"unexpected tracking declaration: {bundle}")
        apis = {entry["NSPrivacyAccessedAPIType"]: set(entry["NSPrivacyAccessedAPITypeReasons"])
                for entry in manifest["NSPrivacyAccessedAPITypes"]}
        require(apis.get("NSPrivacyAccessedAPICategoryUserDefaults", set()) >= {"CA92.1", "1C8F.1"},
                f"missing defaults/App Group reason: {bundle}")
        require(manifest["NSPrivacyCollectedDataTypes"] == [{
            "NSPrivacyCollectedDataType": "NSPrivacyCollectedDataTypeDeviceID",
            "NSPrivacyCollectedDataTypeLinked": True,
            "NSPrivacyCollectedDataTypeTracking": False,
            "NSPrivacyCollectedDataTypePurposes": ["NSPrivacyCollectedDataTypePurposeAppFunctionality"],
        }], f"unexpected device-routing data declaration: {bundle}")
        command("codesign", "--verify", "--strict", str(bundle))
        entitlements = plistlib.loads(command("codesign", "-d", "--entitlements", ":-", str(bundle)))
        require(entitlements.get("application-identifier") == "JY32YTS7GQ." + identifier,
                f"wrong signing application identifier: {bundle}")
        require("group.blue.meow.app" in entitlements.get("com.apple.security.application-groups", []),
                f"missing shared group: {bundle}")
        if bundle == app:
            require(info.get("ITSAppUsesNonExemptEncryption") is False, "missing encryption declaration")
            require(entitlements.get("com.apple.developer.usernotifications.time-sensitive") is True,
                    "missing Time Sensitive Notifications entitlement")
        if distribution:
            require(entitlements.get("get-task-allow") is False, f"export permits debugging: {bundle}")
            profile = plistlib.loads(command("security", "cms", "-D", "-i", str(bundle / "embedded.mobileprovision")))
            require(not profile.get("ProvisionedDevices") and not profile.get("ProvisionsAllDevices"),
                    f"not an App Store provisioning profile: {bundle}")
            if bundle == app:
                require(entitlements.get("aps-environment") == "production", "exported APNs environment is not production")
        executable = bundle / info["CFBundleExecutable"]
        result.append({"bundle": identifier, "version": version, "build": build,
                       "privacy_manifest": True, "signature_verified": True,
                       "app_group": "group.blue.meow.app", "aps_environment": entitlements.get("aps-environment"),
                       "time_sensitive": entitlements.get("com.apple.developer.usernotifications.time-sensitive", False),
                       "notification_sound": {"file": sound.name, "duration": duration,
                                              "sha256": hashlib.sha256(sound.read_bytes()).hexdigest()},
                       "debuggable": entitlements.get("get-task-allow"),
                       "executable_sha256": hashlib.sha256(executable.read_bytes()).hexdigest()})
    require(result[0]["notification_sound"]["sha256"] == result[1]["notification_sound"]["sha256"],
            "app and notification extension contain different sounds")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("artifact", type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--build", required=True)
    args = parser.parse_args()
    distribution = args.artifact.suffix == ".ipa"
    if distribution:
        with tempfile.TemporaryDirectory(prefix="meow-ipa-check-") as tmp:
            with zipfile.ZipFile(args.artifact) as archive:
                archive.extractall(tmp)
            apps = list((Path(tmp) / "Payload").glob("*.app"))
            require(len(apps) == 1, "expected one app in IPA")
            bundles = verify(apps[0], True, args.version, args.build)
    else:
        apps = list((args.artifact / "Products/Applications").glob("*.app"))
        require(len(apps) == 1, "expected one app in archive")
        bundles = verify(apps[0], False, args.version, args.build)
    print(json.dumps({"artifact": str(args.artifact.resolve()), "distribution_signed": distribution,
                      "uploaded_to_testflight": "not checked", "bundles": bundles}, indent=2))


if __name__ == "__main__":
    main()

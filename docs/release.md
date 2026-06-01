# Release setup (one-time, Paul-only)

These steps prepare an Apple Developer account + signing infrastructure
for `task release:*` and `.github/workflows/release.yml`. Run once;
afterwards every release happens via `git tag v0.X.Y && git push --tags`.

## Apple Developer Program

- Enroll at https://developer.apple.com/programs/ ($99/yr).
- Capture **Team ID** (top-right of https://developer.apple.com/account; 10-char string).

## Developer ID Application certificate

1. Keychain Access → Certificate Assistant → Request a Certificate
   from a Certificate Authority → "Saved to disk." Generates CSR + private key.
2. https://developer.apple.com/account/resources/certificates → "+" →
   "Developer ID Application" → upload CSR → download `.cer` → double-click
   to import. (Skip "Developer ID Installer" — we ship `.dmg`, not `.pkg`.)
3. In Keychain Access, find "Developer ID Application: Paul Tyng (TEAMID)"
   → right-click → Export → `.p12` with a strong password. Stash file +
   password in 1Password.

## App Store Connect API key (preferred over Apple ID + app password)

1. https://appstoreconnect.apple.com/access/integrations/api → "+" →
   role: "Developer" → download `.p8` (one-time download only).
2. Capture **Key ID** (10 chars) and **Issuer ID** (UUID).
3. Stash `.p8` + Key ID + Issuer ID in 1Password.

## Local test

After import:

```sh
SIGNING_IDENTITY="Developer ID Application: Paul Tyng (TEAMID)" \
  task release:sign

APPLE_API_KEY_P8=~/.apple-api-key.p8 \
APPLE_API_KEY_ID=ABCDE12345 \
APPLE_API_KEY_ISSUER_ID=... \
VERSION=0.1.0-test \
  task release:notarize
```

First notarization will likely fail. `xcrun notarytool log <submission-id>
--key ...` shows why. Common causes: missing `--timestamp`, nested binary
without signature.

## GitHub repo secrets (for CI release workflow)

At `paultyng/ideate → Settings → Secrets and variables → Actions`:

- `APPLE_CERT_P12_BASE64` — `base64 -i DeveloperID.p12 | pbcopy`
- `APPLE_CERT_PASSWORD` — p12 export password
- `APPLE_API_KEY_P8_BASE64` — `base64 -i AuthKey_XXX.p8 | pbcopy`
- `APPLE_API_KEY_ID`
- `APPLE_API_KEY_ISSUER_ID`
- `APPLE_SIGNING_IDENTITY` — `Developer ID Application: Paul Tyng (TEAMID)`
- `KEYCHAIN_PASSWORD` — any random string; CI-only

## Calendar reminders

- Developer ID Application cert: valid 5 years. Set reminder for year 4.

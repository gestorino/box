package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"text/template"

	"github.com/google/uuid"
)

// profileName is what an Apple device lists the certificate as (Settings → VPN & Device Management);
// a bare .p12 shows only "Identity Certificate".
const profileName = "Gestorino Cert"

var profileTmpl = template.Must(template.New("profile").Funcs(template.FuncMap{"x": xmlText}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>PayloadCertificateFileName</key>
			<string>{{x .Label}}.p12</string>
			<key>PayloadContent</key>
			<data>{{.P12}}</data>
			<key>PayloadDisplayName</key>
			<string>{{x .Label}}</string>
			<key>PayloadIdentifier</key>
			<string>com.gestorino.device.{{x .Serial}}.identity</string>
			<key>PayloadType</key>
			<string>com.apple.security.pkcs12</string>
			<key>PayloadUUID</key>
			<string>{{.InnerUUID}}</string>
			<key>PayloadVersion</key>
			<integer>1</integer>
		</dict>
	</array>
	<key>PayloadDescription</key>
	<string>Opens your Concierge box from anywhere, on this device only.</string>
	<key>PayloadDisplayName</key>
	<string>{{x .Name}}</string>
	<key>PayloadIdentifier</key>
	<string>com.gestorino.device.{{x .Serial}}</string>
	<key>PayloadOrganization</key>
	<string>Gestorino</string>
	<key>PayloadType</key>
	<string>Configuration</string>
	<key>PayloadUUID</key>
	<string>{{.UUID}}</string>
	<key>PayloadVersion</key>
	<integer>1</integer>
</dict>
</plist>
`))

func xmlText(s string) (string, error) {
	var b bytes.Buffer
	err := xml.EscapeText(&b, []byte(s))
	return b.String(), err
}

// Profile wraps an issued .p12 in an Apple configuration profile (.mobileconfig) named profileName.
// It holds no password: the device asks for the .p12's while installing.
func Profile(label, serial string, p12 []byte) ([]byte, error) {
	var b bytes.Buffer
	err := profileTmpl.Execute(&b, struct{ Name, Label, Serial, P12, UUID, InnerUUID string }{
		profileName, label, serial, base64.StdEncoding.EncodeToString(p12), uuid.NewString(), uuid.NewString(),
	})
	return b.Bytes(), err
}

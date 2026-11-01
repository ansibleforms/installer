// Package version holds the AnsibleForms version the installer installs by default.
package version

// App is the AnsibleForms version the installer installs by default, and the installer's
// own version: installer v7.4.2 installs AnsibleForms 7.4.2. The Follow app release pull
// request moves it with every AnsibleForms release, and the release workflow reads it from
// this line to name the release, so it stays one line of the form const App = "x.y.z".
const App = "7.5.0"

package inspect

// ManifestHash exposes the manifest hash to the tests.
func ManifestHash(m Manifest) string { return manifestHash(m) }

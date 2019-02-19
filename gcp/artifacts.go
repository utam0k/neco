package gcp

var artifacts = artifactSet{
	goVersion:           "1.11.5",
	rktVersion:          "1.30.0",
	etcdVersion:         "3.3.12",
	placematVersion:     "1.3.2",
	customUbuntuVersion: "20190213",
	coreOSVersion:       "1967.5.0",
	debPackages: []string{
		"git",
		"build-essential",
		"less",
		"wget",
		"systemd-container",
		"lldpd",
		"qemu",
		"qemu-kvm",
		"socat",
		"picocom",
		"cloud-utils",
		"xauth",
		"bash-completion",
		"ansible",
		"python-jmespath",
		"dbus",
		"sshpass",
		"jq",
		"libgpgme11",
		"freeipmi-tools",
		"unzip",
		"skopeo",
		"podman",
		"cri-o-runc",
		"fakeroot",
		"btrfs-tools",
		"libdevmapper-dev",
		"libgpgme-dev",
		"libostree-dev",
	},
}

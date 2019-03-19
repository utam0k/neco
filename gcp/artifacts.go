package gcp

var artifacts = artifactSet{
	goVersion:           "1.12.1",
	rktVersion:          "1.30.0",
	etcdVersion:         "3.3.12",
	placematVersion:     "1.3.5",
	customUbuntuVersion: "20190312",
	coreOSVersion:       "2023.4.0",
	ctVersion:           "0.9.0",
	protobufVersion:     "3.7.0",
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
		// required by building container image
		"skopeo",
		"podman",
		"cri-o-runc",
		"cri-o-1.13",
		"fakeroot",
		"btrfs-tools",
		// required by building neco
		"libdevmapper-dev",
		"libgpgme-dev",
		"libostree-dev",
		// required by building containerd
		"libseccomp-dev",
		// required by building protobuf
		"autoconf",
		"automake",
		"libtool",
	},
}

#!/usr/bin/env python3
"""Compare RUNTIME_IMAGE against BASE_IMAGE, checking all layers and CA stores."""

import base64
import datetime
import hashlib
import io
import json
import os
from pathlib import Path
import re
import struct
import subprocess
import tarfile
import tempfile
import urllib.request
import zipfile


pem = re.compile(
    rb"-----BEGIN (TRUSTED CERTIFICATE|CERTIFICATE)-----\s*(.*?)-----END \1-----",
    re.DOTALL,
)
openssl_bundle = "etc/pki/ca-trust/extracted/openssl/ca-bundle.trust.crt"
tls_bundle = "etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"
aaa_root_sha256 = "d7a7a0fb5d7e2731d771e9484ebcdef71d5f0c3e0a2948782bc83ee0ea699ef4"
store_paths = {
    openssl_bundle,
    tls_bundle,
    "etc/pki/ca-trust/extracted/pem/email-ca-bundle.pem",
    "etc/pki/ca-trust/extracted/pem/objsign-ca-bundle.pem",
    "etc/pki/ca-trust/extracted/java/cacerts",
    "etc/pki/ca-trust/extracted/edk2/cacerts.bin",
    "etc/pki/java/cacerts",
    "etc/pki/tls/cert.pem",
}


def pem_blocks(data):
    return {base64.b64decode(match[2]) for match in pem.finditer(data)}


def certificate_der(block):
    size = block[1] & 127
    start = 2 if block[1] < 128 else 2 + size
    length = block[1] if block[1] < 128 else int.from_bytes(block[2:start], "big")
    return block[:start + length]


def read_image(image, excluded=()):
    files, stores = {}, {}
    with tempfile.TemporaryFile() as output:
        subprocess.run(["docker", "image", "save", image], stdout=output, check=True)
        output.seek(0)
        with tarfile.open(fileobj=output) as archive:
            manifest = json.load(archive.extractfile("manifest.json"))[0]
            config = json.load(archive.extractfile(manifest["Config"]))
            for layer_path in manifest["Layers"]:
                with tarfile.open(fileobj=archive.extractfile(layer_path), mode="r|") as layer:
                    for member in layer:
                        if member.isdir():
                            continue
                        path = member.name.removeprefix("./")
                        assert not path.split("/")[-1].startswith(".wh."), path
                        data = layer.extractfile(member).read() if member.isfile() else b""
                        assert not any(cert in data for cert in excluded), (
                            f"Excluded DER certificate in layer {layer_path}: {path}"
                        )
                        assert not any(certificate_der(b) in excluded for b in pem_blocks(data)), (
                            f"Excluded PEM certificate in layer {layer_path}: {path}"
                        )
                        files[path] = (
                            member.type, member.mode & 0o7777, member.uid, member.gid,
                            member.linkname, hashlib.sha256(data).digest(),
                        )
                        if path in store_paths and member.isfile():
                            stores[path] = data
    return config, files, stores


def verify_incident_chains(base_bundle, runtime_bundle):
    # Pin the public certificate-only fixtures; never execute or extract archive contents.
    with urllib.request.urlopen(
        "https://emudhra.we-have-a-problem.com/chains.zip", timeout=30
    ) as response:
        data = response.read(1_000_001)
    assert hashlib.sha256(data).hexdigest() == (
        "8a2edc81b6c51e3ad27d45ca1e441ec84c961e2dee0fc547f2a02c0f74315f95"
    ), "Incident certificate archive checksum mismatch"
    hosts = [
        f"{kind}-{generation}{cross_sign}-emudhra.we-have-a-problem.com"
        for kind in ("dv", "ov", "ev")
        for generation in ("g3b", "g3c")
        for cross_sign in ("", "-aaa")
    ]
    # Freeze verification within fixture validity so expiry cannot masquerade as distrust.
    verification_time = str(int(
        datetime.datetime(2026, 10, 21, tzinfo=datetime.timezone.utc).timestamp()
    ))
    with zipfile.ZipFile(io.BytesIO(data)) as archive, tempfile.TemporaryDirectory() as directory:
        paths = {f"chains/{host}.pem" for host in hosts}
        assert set(archive.namelist()) == paths | {"chains/README.txt"}
        base_file = Path(directory) / "base.pem"
        runtime_file = Path(directory) / "runtime.pem"
        base_file.write_bytes(base_bundle)
        runtime_file.write_bytes(runtime_bundle)
        for host in hosts:
            certificates = list(pem.finditer(archive.read(f"chains/{host}.pem")))
            assert len(certificates) >= 2, f"Incomplete incident chain: {host}"
            leaf_file = Path(directory) / "leaf.pem"
            chain_file = Path(directory) / "chain.pem"
            leaf_file.write_bytes(certificates[0][0] + b"\n")
            chain_file.write_bytes(b"\n".join(cert[0] for cert in certificates[1:]) + b"\n")
            command = [
                "openssl", "verify", "-no-CApath", "-no-CAstore",
                "-purpose", "sslserver", "-verify_hostname", host,
                "-attime", verification_time, "-untrusted", str(chain_file),
            ]
            baseline = subprocess.run(
                command + ["-CAfile", str(base_file), str(leaf_file)],
                capture_output=True, text=True,
            )
            assert baseline.returncode == 0, (
                f"Incident fixture does not validate against original base: {host}\n"
                f"{baseline.stdout}{baseline.stderr}"
            )
            rejected = subprocess.run(
                command + ["-CAfile", str(runtime_file), str(leaf_file)],
                capture_output=True, text=True,
            )
            assert rejected.returncode != 0 and re.search(
                r"error (20|21) at \d+ depth lookup", rejected.stderr
            ), f"Incident chain not rejected for missing trust: {host}\n{rejected.stdout}{rejected.stderr}"
    print("Rejected all 12 incident TLS chains, including all 6 AAA cross-signed chains")


base_config, base_files, base_stores = read_image(os.environ["BASE_IMAGE"])
excluded = set()
for match in pem.finditer(base_stores[openssl_bundle]):
    subject = subprocess.check_output(
        ["openssl", "x509", "-noout", "-subject", "-nameopt", "RFC2253"],
        input=match[0],
    ).lower()
    certificate = certificate_der(base64.b64decode(match[2]))
    if (
        b"emudhra" in subject or b"emsign" in subject
        or hashlib.sha256(certificate).hexdigest() == aaa_root_sha256
    ):
        excluded.add(certificate)

config, files, stores = read_image(os.environ["RUNTIME_IMAGE"], excluded)
assert len(config["rootfs"]["diff_ids"]) == 1, "Only the sanitized layer may be shipped"
assert not set(config["rootfs"]["diff_ids"]) & set(base_config["rootfs"]["diff_ids"])
assert config["architecture"] == base_config["architecture"], "Architecture changed"
assert config["config"]["Env"] == base_config["config"]["Env"], "Environment changed"
assert files.keys() == base_files.keys(), "Runtime file inventory changed"
assert stores.keys() == store_paths, "Not all certificate stores were checked"
for path in files:
    assert files[path][:5] == base_files[path][:5], f"File metadata changed: {path}"
    if path not in store_paths:
        assert files[path] == base_files[path], f"Non-certificate file changed: {path}"
for path in store_paths:
    if path.endswith((".pem", ".crt")):
        expected = {
            block for block in pem_blocks(base_stores[path])
            if certificate_der(block) not in excluded
        }
        assert pem_blocks(stores[path]) == expected, f"Certificate policy changed: {path}"
retained = {certificate_der(block) for block in pem_blocks(stores[tls_bundle])}
assert retained, "TLS trust store is empty"
for path in store_paths:
    if path.endswith(("/cacerts", "/cacerts.bin")):
        assert all(cert in stores[path] for cert in retained), path
    if path.endswith("/cacerts"):
        assert struct.unpack(">III", stores[path][:12]) == (0xFEEDFEED, 2, len(retained)), path
print(
    f"Verified all runtime layers: {len(excluded)} eMudhra/emSign or AAA roots excluded; "
    f"{len(retained)} other TLS roots and non-certificate runtime files preserved"
)
verify_incident_chains(base_stores[tls_bundle], stores[tls_bundle])

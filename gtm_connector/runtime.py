"""Run two HTTPS loopback services with a private, local fixture owner."""

from contextlib import ExitStack, closing
from datetime import datetime, timedelta, timezone
import fcntl
import ipaddress
import json
import os
from pathlib import Path
import re
import socket
import stat
import tempfile
import threading
import time

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID
import uvicorn

from . import calls, slack, store
from .auth import LocalAuthority, create_auth_app
from .gateway import Connector, require_distinct_databases


def private_file(path, content):
    """Create/replace a regular file without following a symlink."""
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "wb") as output:
        os.fchmod(output.fileno(), 0o600)
        output.write(content)


def certificate(directory):
    key = ec.generate_private_key(ec.SECP256R1())
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "Local connector fixture")])
    now = datetime.now(timezone.utc)
    cert = (x509.CertificateBuilder().subject_name(name).issuer_name(name)
            .public_key(key.public_key()).serial_number(x509.random_serial_number())
            .not_valid_before(now - timedelta(minutes=1)).not_valid_after(now + timedelta(days=1))
            .add_extension(x509.SubjectAlternativeName([
                x509.IPAddress(ipaddress.ip_address("127.0.0.1"))]), critical=False)
            .sign(key, hashes.SHA256()))
    key_path, cert_path = directory / "tls.key", directory / "tls.pem"
    private_file(key_path, key.private_bytes(serialization.Encoding.PEM,
                 serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
    private_file(cert_path, cert.public_bytes(serialization.Encoding.PEM))
    return key_path, cert_path


class StateInUseError(ValueError):
    """Another connector owns the requested local state folder."""


class SourceIdentityError(ValueError):
    """Refuse unsafe local source reuse without disclosing the connected identity."""

    code = "source_identity_mismatch"


def _unique_marker(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError
        result[key] = value
    return result


def _read_marker(path, *, require_schema=True):
    """Read bounded private metadata without following links or changing it."""
    try:
        info = path.lstat()
    except FileNotFoundError:
        return None
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor) as source:
        opened = os.fstat(source.fileno())
        if (not stat.S_ISREG(opened.st_mode) or opened.st_uid != os.getuid()
                or opened.st_mode & 0o077 or opened.st_size > 4096):
            raise ValueError
        marker = json.loads(source.read(4097), object_pairs_hook=_unique_marker)
    if not isinstance(marker, dict) or (require_schema and type(marker.get("schema_version")) is not int):
        raise ValueError
    return marker


def _write_marker(path, marker):
    """Publish one complete private marker; a failed replacement leaves no partial file."""
    content = json.dumps(marker, sort_keys=True).encode("utf-8")
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".source-pin-", delete=False) as output:
            temporary = Path(output.name)
            os.fchmod(output.fileno(), 0o600)
            output.write(content)
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def _valid_connection_identity(identity):
    return (isinstance(identity, dict)
            and set(identity) == {"schema_version", "backend", "instance_url", "organization_id", "environment"}
            and type(identity["schema_version"]) is int and identity["schema_version"] == 1
            and identity["backend"] == "salesforce"
            and isinstance(identity["instance_url"], str) and 1 <= len(identity["instance_url"]) <= 512
            and isinstance(identity["organization_id"], str)
            and re.fullmatch(r"00D[A-Za-z0-9]{15}", identity["organization_id"]) is not None
            and identity["environment"] in ("developer", "sandbox", "production"))


def _prepare_demo_state(directory, binding):
    """Validate a demo binding and return a missing marker for later publication."""
    path = directory / "source-profile.json"
    try:
        info = path.lstat()
    except FileNotFoundError:
        info = None
    if binding is None:
        if info is not None:
            raise ValueError("This state directory belongs to a Salesforce demo binding. "
                             "Pass the same --salesforce-demo-binding or choose a fresh --state-dir.")
        return
    expected = {"schema_version": 1, "kit_id": binding.kit_id,
                "manifest_sha256": binding.manifest_sha256, "binding_sha256": binding.binding_sha256}
    if info is not None:
        try:
            if _read_marker(path) != expected:
                raise ValueError
        except (OSError, ValueError, UnicodeError, RecursionError):
            raise ValueError("The source profile does not match this private Salesforce demo binding. "
                             "Use the original binding or choose a fresh --state-dir.") from None
        return
    if any(child.name not in ("server.lock", "crm-connection.json") for child in directory.iterdir()):
        raise ValueError("A Salesforce demo binding requires fresh state. Choose an empty --state-dir.")
    return expected


def _prepare_source_state(directory, reader, binding):
    """Validate both source selections before publishing either marker or source data."""
    path = directory / "crm-connection.json"
    if reader is None:
        connected_state = os.path.lexists(path)
        if not connected_state:
            try:
                owner = _read_marker(directory / "owner.json", require_schema=False)
                if owner is not None:
                    if "crm_backend" not in owner:
                        # Before Salesforce support, this complete owner shape
                        # represented SQLite. An incomplete document proves no mode.
                        legacy_keys = {"issuer", "resource", "cert_path", "client_id", "redirect_uri", "owner_secret"}
                        if set(owner) != legacy_keys or any(not isinstance(value, str) or not value
                                                            for value in owner.values()):
                            raise ValueError
                    elif owner["crm_backend"] not in ("sqlite", "salesforce"):
                        raise ValueError
                    connected_state = owner.get("crm_backend", "sqlite") == "salesforce"
            except (OSError, ValueError, UnicodeError, RecursionError):
                raise SourceIdentityError("The prior local backend cannot be verified from private owner metadata. "
                                          "Use a fresh --state-dir.") from None
        if connected_state:
            raise SourceIdentityError("This state folder belongs to a Salesforce connection or demo binding. "
                                      "Select its original connection or use a fresh --state-dir.")
    demo_marker = _prepare_demo_state(directory, binding)
    if reader is None:
        return None
    try:
        marker = _read_marker(path)
        if marker is not None and not _valid_connection_identity(marker):
            raise ValueError
    except (OSError, ValueError, UnicodeError, RecursionError):
        raise SourceIdentityError("The private Salesforce connection marker is invalid. "
                                  "Use the original private state or a fresh --state-dir.") from None
    if marker is None and any(child.name not in ("server.lock", "source-profile.json")
                              for child in directory.iterdir()):
        raise SourceIdentityError("This state folder has no pinned Salesforce connection. "
                                  "Use fresh state with a different --state-dir.")
    identity = reader.prepare_connection()
    if not _valid_connection_identity(identity):
        raise SourceIdentityError("The selected Salesforce connection did not provide a usable identity. "
                                  "Run gtm-connector salesforce check before starting it again.")
    if marker is not None and marker != identity:
        raise SourceIdentityError("This state folder belongs to a different Salesforce connection or environment. "
                                  "Select its original connection or use a fresh --state-dir.")
    try:
        if marker is None:
            _write_marker(path, identity)
        if demo_marker is not None:
            _write_marker(directory / "source-profile.json", demo_marker)
    except OSError:
        raise SourceIdentityError("The private source markers could not be saved. "
                                  "Check the state folder permissions, then retry with the same connection.") from None
    return identity


def _seed_demo_sources(directory, binding):
    """Validate every seed before committing; a seed error rolls back all three."""
    with ExitStack() as stack:
        pending = []
        for name, module in (("crm", store), ("calls", calls), ("slack", slack)):
            path = directory / f"{name}.sqlite3"
            if path.is_symlink():
                raise ValueError("source databases must not be symbolic links")
            connection = stack.enter_context(closing(module.connect(path)))
            path.chmod(0o600)
            connection.execute("BEGIN")
            module.initialize(connection)
            if name == "calls":
                binding.seed_calls(connection)
            elif name == "slack":
                binding.seed_slack(connection)
            else:
                module.seed(connection)
            pending.append((path, connection))
        for path, connection in pending:
            connection.commit()
            path.chmod(0o600)


class Runtime:
    """One process, two ports, four databases; owns shutdown and a state lock."""

    def __init__(self, directory, *, rate_limit=60, crm_reader=None, on_call=None, demo_binding=None):
        self.directory = Path(directory).resolve()
        self.rate_limit = rate_limit
        self.crm_reader = crm_reader
        self.on_call = on_call
        self.crm_backend = "sqlite" if crm_reader is None else crm_reader.name
        self.demo_binding = demo_binding
        self.crm_connection_identity = None
        if demo_binding is not None and self.crm_backend != "salesforce":
            raise ValueError("--salesforce-demo-binding requires --crm-backend salesforce")
        self.servers, self.threads, self.sockets = [], [], []
        self.lock = None

    def __enter__(self):
        try:
            self.directory.mkdir(parents=True, mode=0o700, exist_ok=True)
            self.directory.chmod(0o700)
            descriptor = os.open(self.directory / "server.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
            self.lock = os.fdopen(descriptor, "wb")
            try:
                fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise StateInUseError("this state directory already has a running connector; "
                                      "stop it with Ctrl-C or choose another --state-dir") from exc
            self.crm_connection_identity = _prepare_source_state(self.directory, self.crm_reader, self.demo_binding)
            require_distinct_databases([self.directory / f"{name}.sqlite3"
                                        for name in ("crm", "calls", "slack", "audit")])
            if self.demo_binding is not None:
                _seed_demo_sources(self.directory, self.demo_binding)
            else:
                for name, module in (("crm", store), ("calls", calls), ("slack", slack)):
                    path = self.directory / f"{name}.sqlite3"
                    if path.is_symlink():
                        raise ValueError("source databases must not be symbolic links")
                    with closing(module.connect(path)) as connection:
                        module.initialize(connection)
                        module.seed(connection)
                    path.chmod(0o600)
            key, cert = certificate(self.directory)
            for _ in range(2):
                sock = socket.socket()
                sock.bind(("127.0.0.1", 0))
                sock.listen(128)
                self.sockets.append(sock)
            issuer = f"https://127.0.0.1:{self.sockets[0].getsockname()[1]}"
            resource = f"https://127.0.0.1:{self.sockets[1].getsockname()[1]}/mcp"
            authority = LocalAuthority(issuer, resource)
            self.config = {"issuer": issuer, "resource": resource, "cert_path": str(cert),
                           "client_id": authority.client_id, "redirect_uri": authority.redirect_uri,
                           "owner_secret": authority.owner_secret, "crm_backend": self.crm_backend}
            if self.crm_connection_identity is not None:
                self.config["sf_environment"] = self.crm_connection_identity["environment"]
            self.connector = Connector(self.directory / "crm.sqlite3", self.directory / "calls.sqlite3",
                                       self.directory / "audit.sqlite3", authority, rate_limit=self.rate_limit,
                                       slack_path=self.directory / "slack.sqlite3", crm_reader=self.crm_reader,
                                       on_call=self.on_call, demo_binding=self.demo_binding)
            for app, sock in zip((create_auth_app(authority), self.connector), self.sockets):
                server = uvicorn.Server(uvicorn.Config(app, ssl_keyfile=str(key), ssl_certfile=str(cert),
                    log_config=None, access_log=False, log_level="warning", lifespan="on"))
                thread = threading.Thread(target=server.run, kwargs={"sockets": [sock]}, daemon=True)
                self.servers.append(server)
                self.threads.append(thread)
                thread.start()
            deadline = time.monotonic() + 10
            while not all(server.started for server in self.servers):
                if time.monotonic() > deadline or not all(thread.is_alive() for thread in self.threads):
                    raise RuntimeError("local HTTPS services did not start")
                time.sleep(0.02)
            private_file(self.directory / "owner.json", json.dumps(self.config).encode())
            return self
        except BaseException:
            self.__exit__(None, None, None)
            raise

    def __exit__(self, *_):
        for server in self.servers:
            server.should_exit = True
        for thread in self.threads:
            thread.join(timeout=10)
        for sock in self.sockets:
            sock.close()
        if self.lock is not None:
            self.lock.close()

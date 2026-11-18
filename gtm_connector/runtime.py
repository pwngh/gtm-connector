"""Run two HTTPS loopback services with a private, local fixture owner."""

from contextlib import ExitStack, closing
from datetime import datetime, timedelta, timezone
import fcntl
import ipaddress
import json
import os
from pathlib import Path
import socket
import stat
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


def _prepare_demo_state(directory, binding):
    """Pin local source identity under the state lock before opening databases."""
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
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
                raise ValueError
            descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(descriptor) as source:
                opened = os.fstat(source.fileno())
                if (not stat.S_ISREG(opened.st_mode) or opened.st_uid != os.getuid()
                        or opened.st_mode & 0o077 or opened.st_size > 4096):
                    raise ValueError
                marker = json.loads(source.read(4097))
            if not isinstance(marker, dict) or type(marker.get("schema_version")) is not int or marker != expected:
                raise ValueError
        except (OSError, ValueError, UnicodeError):
            raise ValueError("The source profile does not match this private Salesforce demo binding. "
                             "Use the original binding or choose a fresh --state-dir.") from None
        return
    if any(child.name != "server.lock" for child in directory.iterdir()):
        raise ValueError("A Salesforce demo binding requires fresh state. Choose an empty --state-dir.")
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "w") as output:
        json.dump(expected, output, sort_keys=True)


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
            _prepare_demo_state(self.directory, self.demo_binding)
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

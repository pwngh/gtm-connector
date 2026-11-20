"""Run two HTTPS loopback services with a private, local fixture owner."""

from contextlib import ExitStack, closing, contextmanager
from copy import deepcopy
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
import fcntl
import hashlib
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
from . import strict_json
from .auth import LocalAuthority, create_auth_app
from .gateway import Connector, require_distinct_databases


_FIXTURE_CRM_IDENTITY = {"schema_version": 1, "backend": "sqlite", "fixture_book": "renewal-book-v2"}
_DATABASE_NAMES = ("crm", "calls", "slack", "audit", "imports")
# What connect leaves in a folder before any source is pinned to it: the profile, the seller list,
# and the Slack sign-in.
_UNPINNED = {"server.lock", "profile.json", "sellers.json", "slack-connection"}
_INITIAL_METADATA = _UNPINNED | {"crm-connection.json", "calls-source.json", "slack-source.json",
                                 "slack-mappings.json"}


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


def _read_marker(path, *, require_schema=True):
    """Read bounded private metadata without following links or changing it."""
    try:
        info = path.lstat()
    except FileNotFoundError:
        return None
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_nlink != 1:
        raise ValueError
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor) as source:
        opened = os.fstat(source.fileno())
        if (not stat.S_ISREG(opened.st_mode) or opened.st_uid != os.getuid()
                or opened.st_mode & 0o077 or opened.st_size > 4096 or opened.st_nlink != 1
                or (opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino)):
            raise ValueError
        marker = strict_json.loads(source.read(4097))
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


def _calls_binding(identity):
    document = _FIXTURE_CRM_IDENTITY if identity is None else identity
    canonical = json.dumps(document, sort_keys=True, separators=(",", ":"),
                           ensure_ascii=False, allow_nan=False).encode("utf-8")
    return {"schema_version": 1, "crm_backend": document["backend"],
            "identity_sha256": hashlib.sha256(canonical).hexdigest()}


def _valid_calls_marker(marker):
    if (not isinstance(marker, dict) or set(marker) != {"schema_version", "backend", "crm_binding"}
            or type(marker["schema_version"]) is not int or marker["schema_version"] != 1
            or marker["backend"] != "imported"):
        return False
    binding = marker["crm_binding"]
    return (isinstance(binding, dict) and set(binding) == {"schema_version", "crm_backend", "identity_sha256"}
            and type(binding["schema_version"]) is int and binding["schema_version"] == 1
            and binding["crm_backend"] in ("sqlite", "salesforce")
            and isinstance(binding["identity_sha256"], str)
            and re.fullmatch(r"[0-9a-f]{64}", binding["identity_sha256"]) is not None)


def _prepare_calls_selection(directory, backend):
    """Reject a calls mode switch before reading CRM metadata or changing source state."""
    try:
        marker = _read_marker(directory / "calls-source.json")
        if marker is not None and not _valid_calls_marker(marker):
            raise ValueError
    except (OSError, ValueError, UnicodeError, RecursionError):
        raise SourceIdentityError("The private calls source metadata is invalid. "
                                  "Use the original private state or a fresh --state-dir.") from None
    try:
        owner = _read_marker(directory / "owner.json", require_schema=False)
        prior_backend = owner.get("calls_backend") if owner is not None else None
        if owner is not None and (prior_backend not in ("fixture", "imported", "none")
                                  or owner.get("crm_backend") not in ("sqlite", "salesforce")
                                  or owner.get("slack_backend") not in ("fixture", "slack", "none")):
            raise ValueError
    except (OSError, ValueError, UnicodeError, RecursionError):
        raise SourceIdentityError("The prior local backend cannot be verified from private owner metadata. "
                                  "Use a fresh --state-dir.") from None
    if backend == "none":
        # Not connected: nothing is pinned or seeded, so imported transcripts can be connected later.
        return None
    if backend == "fixture":
        if (marker is not None or prior_backend in ("imported", "none")
                or any(os.path.lexists(directory / name) for name in ("imports.sqlite3", "imports.sqlite3-journal"))):
            raise SourceIdentityError("This state folder uses imported transcripts. "
                                      "Run gtm-connector connect calls or use a fresh --state-dir.")
        return None
    if os.path.lexists(directory / "calls.sqlite3") or prior_backend == "fixture":
        raise SourceIdentityError("Imported transcripts cannot reuse fixture calls. Use a fresh --state-dir.")
    if marker is None and prior_backend != "none" and any(child.name not in _INITIAL_METADATA
                                                          for child in directory.iterdir()):
        raise SourceIdentityError("This state folder has no pinned imported calls source. "
                                  "Use fresh state with another --state-dir.")
    if marker is not None and not os.path.lexists(directory / "imports.sqlite3") and any(
            child.name not in _INITIAL_METADATA
            for child in directory.iterdir()):
        raise SourceIdentityError("The imported calls database is missing from existing state. "
                                  "Restore the original private state or select a fresh --state-dir.")
    return marker


def _new_calls_marker(backend, marker, identity):
    if backend != "imported":
        return None
    expected = {"schema_version": 1, "backend": "imported", "crm_binding": _calls_binding(identity)}
    if marker is not None and marker != expected:
        raise SourceIdentityError("The imported calls source belongs to a different CRM connection. "
                                  "Select the original connection or a fresh --state-dir.")
    return expected if marker is None else None


def _refuse_salesforce_state(directory):
    """Demo CRM data never opens in a folder that belongs to a Salesforce connection."""
    connected_state = os.path.lexists(directory / "crm-connection.json")
    if not connected_state:
        try:
            owner = _read_marker(directory / "owner.json", require_schema=False)
            if owner is not None:
                if owner.get("crm_backend") not in ("sqlite", "salesforce"):
                    raise ValueError
                connected_state = owner["crm_backend"] == "salesforce"
        except (OSError, ValueError, UnicodeError, RecursionError):
            raise SourceIdentityError("The prior local backend cannot be verified from private owner metadata. "
                                      "Use a fresh --state-dir.") from None
    if connected_state:
        raise SourceIdentityError("This state folder belongs to a Salesforce connection. "
                                  "Select its original connection or use a fresh --state-dir.")


def check_folder(directory, *, demo_crm, calls_backend=None, slack_backend=None, slack_connection_dir=None):
    """Refuse sources that what this folder already read rules out. Nothing is written or contacted.

    A source given as None is not changing and is not checked, so a problem with one source never blocks
    connecting another.
    """
    directory = _state_directory(directory)
    if directory.is_dir():
        if demo_crm:
            _refuse_salesforce_state(directory)
        if calls_backend is not None:
            _prepare_calls_selection(directory, calls_backend)
        if slack_backend is not None:
            _prepare_slack_selection(directory, slack_backend, slack_connection_dir)


# What connect may leave in a folder it created and then could not connect.
_CONNECT_FILES = ("server.lock", "profile.json", "sellers.json", "crm-connection.json", "calls-source.json")


@contextmanager
def changing_sources(directory):
    """Hold the folder's state lock while the sources it is connected to change.

    A running connector holds the same lock, so a folder being served refuses, and two changes never
    interleave. A folder this creates is removed again when the change fails.
    """
    directory = _state_directory(directory)
    if directory.is_symlink():
        raise SourceIdentityError("The state folder must not be a symbolic link. Use a private --state-dir.")
    created = not os.path.lexists(directory)
    directory.mkdir(parents=True, mode=0o700, exist_ok=True)
    try:
        descriptor = os.open(directory / "server.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        with os.fdopen(descriptor, "wb") as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                raise StateInUseError("This state folder has a running connector or another command. "
                                      "Stop it, then connect again.") from None
            yield directory
    except BaseException:
        if created:
            for name in _CONNECT_FILES:
                (directory / name).unlink(missing_ok=True)
            try:
                directory.rmdir()
            except OSError:
                pass
        raise


def pin_crm(directory, reader, *, calls_backend):
    """Check a Salesforce connection's identity and pin it to the folder, as starting the services does, without
    starting them. The caller holds the folder's lock."""
    return _prepare_source_state(_state_directory(directory), reader, calls_backend=calls_backend)


def held_sources(directory):
    """The transcripts and Slack thread a folder has already read. Demo data never stands in for them."""
    directory = _state_directory(directory)
    held = {}
    if any(os.path.lexists(directory / name) for name in ("calls-source.json", "imports.sqlite3")):
        held["calls"] = {"provider": "imported"}
    if any(os.path.lexists(directory / name) for name in ("slack-source.json", "slack-mappings.json")):
        held["slack"] = {"provider": "slack"}
    return held


def demo_book(directory):
    """The folder's own copy of the demo book, or None before its services have run. Nothing is written.

    A folder pinned to Salesforce has no demo book, and the book must be the folder's own private file.
    """
    directory = _state_directory(directory)
    if not os.path.lexists(directory):
        return None
    if directory.is_symlink():
        raise SourceIdentityError("The state folder must not be a symbolic link. Use a private --state-dir.")
    _refuse_salesforce_state(directory)
    path = directory / "crm.sqlite3"
    if not os.path.lexists(path):
        return None
    try:
        _private_existing(path)
        if path.lstat().st_nlink != 1:
            raise ValueError
    except (OSError, ValueError):
        raise SourceIdentityError("The folder's CRM database must be its own private file, not a link. "
                                  "Use a fresh --state-dir.") from None
    return path


def _prepare_source_state(directory, reader, *, calls_backend="fixture"):
    """Validate both source selections before publishing either marker or source data."""
    path = directory / "crm-connection.json"
    marker = None
    if reader is not None:
        try:
            marker = _read_marker(path)
            if marker is not None and not _valid_connection_identity(marker):
                raise ValueError
        except (OSError, ValueError, UnicodeError, RecursionError):
            raise SourceIdentityError("The private Salesforce connection marker is invalid. "
                                      "Use the original private state or a fresh --state-dir.") from None
    calls_marker = _prepare_calls_selection(directory, calls_backend)
    if reader is None:
        _refuse_salesforce_state(directory)
        new_calls = _new_calls_marker(calls_backend, calls_marker, None)
        if new_calls is not None:
            try:
                _write_marker(directory / "calls-source.json", new_calls)
            except OSError:
                raise SourceIdentityError("The private calls source marker could not be saved. "
                                          "Check private folder permissions, then retry the same selection.") from None
        return None
    if marker is None and any(child.name not in _UNPINNED for child in directory.iterdir()):
        raise SourceIdentityError("This state folder has no pinned Salesforce connection. "
                                  "Use fresh state with a different --state-dir.")
    identity = reader.prepare_connection()
    if not _valid_connection_identity(identity):
        raise SourceIdentityError("The selected Salesforce connection did not provide a usable identity. "
                                  "Run gtm-connector salesforce check before starting it again.")
    if marker is not None and marker != identity:
        raise SourceIdentityError("This state folder belongs to a different Salesforce connection or environment. "
                                  "Select its original connection or use a fresh --state-dir.")
    new_calls = _new_calls_marker(calls_backend, calls_marker, identity)
    try:
        if marker is None:
            _write_marker(path, identity)
        if new_calls is not None:
            _write_marker(directory / "calls-source.json", new_calls)
    except OSError:
        raise SourceIdentityError("The private source markers could not be saved. "
                                  "Check the state folder permissions, then retry with the same connection.") from None
    return identity


def valid_sellers(sellers):
    return (isinstance(sellers, list) and len(sellers) <= 200
            and all(isinstance(seller, str) and 1 <= len(seller) <= 200 for seller in sellers))


def saved_sellers(directory):
    """The seller list connect saved for a folder, for a person to check."""
    return list(_sellers(_state_directory(directory)))


def _sellers(directory):
    """The identities the operator lists as sellers in sellers.json. Without the file there are none."""
    try:
        document = strict_json.loads((directory / "sellers.json").read_bytes())
    except FileNotFoundError:
        return ()
    except (OSError, ValueError, RecursionError):
        document = None
    if (not isinstance(document, dict) or set(document) != {"schema_version", "sellers"}
            or document["schema_version"] != 1 or not valid_sellers(document["sellers"])):
        raise SourceIdentityError('sellers.json must be {"schema_version": 1, "sellers": ["a name or Slack ID"]} '
                                  "with at most 200 entries.")
    return tuple(document["sellers"])


def save_sellers(directory, sellers):
    """Replace the seller list of an existing state folder. The reader's checks apply."""
    if not valid_sellers(sellers):
        raise ValueError("Name at most 200 sellers, each a name or Slack ID of 1 to 200 characters.")
    _write_marker(_state_directory(directory) / "sellers.json", {"schema_version": 1, "sellers": sellers})


def _state_directory(directory):
    path = Path(directory).absolute()
    return path.resolve() if path.name == ".." else path.parent.resolve() / path.name


def _private_existing(path, *, directory=False):
    info = path.lstat()
    correct_type = stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)
    if not correct_type or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError


@dataclass(frozen=True)
class ImportState:
    directory: Path
    calls_binding: dict
    imports_path: Path
    crm_backend: str


def _inspect_import_state(directory):
    """Inspect only private pins and store metadata; do not read transcript blobs."""
    from .calls_import import validate_import_store

    directory = _state_directory(directory)
    try:
        _private_existing(directory, directory=True)
        marker = _prepare_calls_selection(directory, "imported")
        if marker is None:
            raise ValueError
        identity = _read_marker(directory / "crm-connection.json")
        if marker["crm_binding"]["crm_backend"] == "salesforce":
            if not _valid_connection_identity(identity):
                raise ValueError
        elif identity is not None:
            raise ValueError
        _new_calls_marker("imported", marker, identity)
        require_distinct_databases([directory / f"{name}.sqlite3" for name in _DATABASE_NAMES])
        imports_path = directory / "imports.sqlite3"
        _private_existing(imports_path)
    except (OSError, ValueError, UnicodeError, RecursionError):
        raise SourceIdentityError("Imported calls state or its private CRM binding is invalid. "
                                  "Use the original private state or a fresh --state-dir.") from None
    validate_import_store(imports_path, crm_binding=marker["crm_binding"])
    return ImportState(directory, dict(marker["crm_binding"]), imports_path, marker["crm_binding"]["crm_backend"])


@contextmanager
def open_import_state(directory):
    """Lock and validate existing imports without creating state or contacting a provider."""
    directory = _state_directory(directory)
    try:
        _private_existing(directory, directory=True)
        lock_path = directory / "server.lock"
        _private_existing(lock_path)
        descriptor = os.open(lock_path, os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK)
    except (OSError, ValueError):
        raise SourceIdentityError("Existing private imported calls state is required. "
                                  "Run gtm-connector calls import --help before selecting its --state-dir.") from None
    with os.fdopen(descriptor, "r+b") as lock:
        opened = os.fstat(lock.fileno())
        if not stat.S_ISREG(opened.st_mode) or opened.st_uid != os.getuid() or opened.st_mode & 0o077:
            raise SourceIdentityError("The existing state lock is not private. Use the original private state.")
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise StateInUseError("This state directory already has a running connector; "
                                  "stop it with Ctrl-C before inspecting imports.") from None
        yield _inspect_import_state(directory)


def _valid_slack_marker(marker):
    from .slack_mapping import valid_crm_binding, valid_slack_binding
    return (isinstance(marker, dict)
            and set(marker) == {"schema_version", "backend", "crm_binding", "slack_binding"}
            and type(marker["schema_version"]) is int and marker["schema_version"] == 1
            and marker["backend"] == "slack" and valid_crm_binding(marker["crm_binding"])
            and valid_slack_binding(marker["slack_binding"]))


def _validate_saved_slack_crm(directory, marker):
    identity = _read_marker(directory / "crm-connection.json")
    if marker["crm_binding"]["crm_backend"] == "salesforce":
        if not _valid_connection_identity(identity):
            raise ValueError
    elif identity is not None:
        raise ValueError
    if marker["crm_binding"] != _calls_binding(identity):
        raise ValueError


def _prepare_slack_selection(directory, backend, connection_dir, expected_binding=None):
    from .slack_mapping import SlackMappingError, slack_identity_binding
    try:
        marker = _read_marker(directory / "slack-source.json")
        if marker is not None and not _valid_slack_marker(marker):
            raise ValueError
        if marker is not None:
            _validate_saved_slack_crm(directory, marker)
    except (OSError, ValueError, UnicodeError, RecursionError):
        raise SlackMappingError("slack_mapping_invalid") from None
    try:
        owner = _read_marker(directory / "owner.json", require_schema=False)
        prior = owner.get("slack_backend") if owner is not None else None
        if owner is not None and prior not in ("fixture", "slack", "none"):
            raise ValueError
    except (OSError, ValueError, UnicodeError, RecursionError):
        if backend == "fixture" and marker is None and not os.path.lexists(directory / "slack-mappings.json"):
            # CRM and calls preparation reports this unusable owner file with its own error.
            return None, None
        raise SlackMappingError("slack_mapping_invalid") from None
    if backend == "none":
        # Not connected: nothing is pinned or seeded, so Slack can be connected later.
        return None, None
    if backend == "fixture":
        if (marker is not None or prior in ("slack", "none")
                or os.path.lexists(directory / "slack-mappings.json")):
            raise SlackMappingError("slack_source_mismatch")
        return None, None
    if prior == "fixture" or os.path.lexists(directory / "slack.sqlite3"):
        raise SlackMappingError("slack_source_mismatch")
    if marker is None and prior != "none" and any(
            child.name not in _UNPINNED | {"crm-connection.json", "calls-source.json"}
            for child in directory.iterdir()):
        raise SlackMappingError("slack_source_mismatch")
    if marker is not None and not os.path.lexists(directory / "slack-mappings.json") and any(
            child.name not in _INITIAL_METADATA - {"slack-mappings.json"} for child in directory.iterdir()):
        raise SlackMappingError("slack_mapping_invalid")
    from .slack_auth import SlackAuthError, inspect_slack_connection
    selection = inspect_slack_connection(connection_dir)
    binding = slack_identity_binding(selection.identity)
    if expected_binding is not None and binding != expected_binding:
        raise SlackMappingError("slack_source_mismatch")
    if selection.needs_login:
        raise SlackAuthError("slack_reconnect_required")
    if selection.credential_state != "ready":
        raise SlackMappingError("slack_identity_unchecked")
    if marker is not None and marker["slack_binding"] != binding:
        raise SlackMappingError("slack_source_mismatch")
    return marker, binding


def _finish_slack_selection(directory, marker, crm_binding, slack_binding):
    from .slack_mapping import SlackMappingError, initialize_slack_mapping_store
    expected = {"schema_version": 1, "backend": "slack", "crm_binding": crm_binding, "slack_binding": slack_binding}
    if marker is not None and marker != expected:
        raise SlackMappingError("slack_source_mismatch")
    try:
        if marker is None:
            _write_marker(directory / "slack-source.json", expected)
        initialize_slack_mapping_store(directory / "slack-mappings.json", crm_binding=crm_binding,
                                       slack_binding=slack_binding)
    except OSError:
        raise SlackMappingError("slack_storage_failed") from None


def _selected_slack_reader(reader, mapping_path, connection_dir, crm_binding, slack_binding, on_read):
    from .slack_mapping import SlackMappingError
    if reader is None:
        from .slack_reader import SlackThreadReader
        return SlackThreadReader(mapping_path, connection_dir=connection_dir, crm_binding=crm_binding,
                                 slack_binding=slack_binding, on_read=on_read)
    try:
        if (reader.name != "slack" or Path(reader.mapping_path).absolute() != mapping_path
                or Path(reader.connection_dir).absolute() != connection_dir
                or reader.crm_binding != crm_binding or reader.slack_binding != slack_binding):
            raise ValueError
    except (AttributeError, TypeError, ValueError):
        raise SlackMappingError("slack_source_mismatch") from None
    if on_read is not None:
        reader.on_read = on_read
    return reader


@dataclass(frozen=True)
class SlackState:
    directory: Path
    crm_binding: dict
    slack_binding: dict
    mapping_path: Path
    crm_backend: str
    mapping_count: int


def inspect_slack_state(directory):
    """Validate saved association metadata without opening CRM, calls, or credentials."""
    from .slack_mapping import SlackMappingError, validate_slack_mapping_store
    directory = _state_directory(directory)
    try:
        _private_existing(directory, directory=True)
        marker = _read_marker(directory / "slack-source.json")
        if not _valid_slack_marker(marker) or os.path.lexists(directory / "slack.sqlite3"):
            raise ValueError
        _validate_saved_slack_crm(directory, marker)
    except (OSError, ValueError, UnicodeError, RecursionError):
        raise SlackMappingError("slack_mapping_invalid") from None
    count = validate_slack_mapping_store(directory / "slack-mappings.json", crm_binding=marker["crm_binding"],
                                         slack_binding=marker["slack_binding"])
    return SlackState(directory, dict(marker["crm_binding"]), dict(marker["slack_binding"]),
                      directory / "slack-mappings.json", marker["crm_binding"]["crm_backend"], count)


class _LocalHTTPS:
    """Own only the existing two-port HTTPS services, with no source preparation."""

    def __init__(self, tls_directory, connector_factory):
        self.tls_directory, self.connector_factory = tls_directory, connector_factory
        self.servers, self.threads, self.sockets = [], [], []

    def __enter__(self):
        try:
            key, cert = certificate(self.tls_directory)
            for _ in range(2):
                sock = socket.socket()
                self.sockets.append(sock)
                sock.bind(("127.0.0.1", 0))
                sock.listen(128)
            issuer = f"https://127.0.0.1:{self.sockets[0].getsockname()[1]}"
            resource = f"https://127.0.0.1:{self.sockets[1].getsockname()[1]}/mcp"
            authority = LocalAuthority(issuer, resource)
            self.config = {"issuer": issuer, "resource": resource, "cert_path": str(cert),
                           "client_id": authority.client_id, "redirect_uri": authority.redirect_uri,
                           "owner_secret": authority.owner_secret}
            self.connector = self.connector_factory(authority)
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
            return self
        except BaseException:
            self.__exit__(None, None, None)
            raise

    def __exit__(self, *_):
        for server in self.servers:
            server.should_exit = True
        for thread in self.threads:
            if thread.ident is not None:
                # A source read may still be running. Keep the state lock and TLS files until it ends.
                thread.join()
        for sock in self.sockets:
            sock.close()


class SlackCheckRuntime:
    """Check saved Slack associations with ephemeral HTTPS and no CRM/calls source access."""

    def __init__(self, directory, *, slack_connection_dir, _slack_reader=None, on_call=None, on_source_read=None):
        self.directory = _state_directory(directory)
        self.slack_connection_dir = Path(slack_connection_dir).expanduser().absolute()
        self.slack_reader = _slack_reader
        self.slack_backend = "slack"
        self.on_call, self.on_source_read = on_call, on_source_read
        self._stack = ExitStack()

    def __enter__(self):
        from .slack_mapping import SlackMappingError
        import sqlite3
        try:
            _private_existing(self.directory, directory=True)
            lock_path = self.directory / "server.lock"
            _private_existing(lock_path)
            descriptor = os.open(lock_path, os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK)
            lock = self._stack.enter_context(os.fdopen(descriptor, "r+b"))
            opened = os.fstat(lock.fileno())
            if (not stat.S_ISREG(opened.st_mode) or opened.st_uid != os.getuid()
                    or opened.st_mode & 0o077 or opened.st_nlink != 1):
                raise SlackMappingError("slack_mapping_invalid")
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                raise StateInUseError("This state directory has a running connector. "
                                      "Stop it before checking Slack.") from None
            state = inspect_slack_state(self.directory)
            self.crm_binding, self.slack_binding = state.crm_binding, state.slack_binding
            self.crm_backend, self.slack_mapping_path = state.crm_backend, state.mapping_path
            audit_path = self.directory / "audit.sqlite3"
            _private_existing(audit_path)
            if audit_path.stat().st_nlink != 1:
                raise SlackMappingError("slack_mapping_invalid")
            with closing(sqlite3.connect(audit_path.as_uri() + "?mode=ro", uri=True)) as audit:
                audit.execute("SELECT audit_id, started_at, token_id, tool, arguments, request_id, brief_id, "
                              "status, http_status, row_count, rows, elapsed_ms, error FROM audit_log WHERE 0")
            self.slack_reader = _selected_slack_reader(
                self.slack_reader, self.slack_mapping_path, self.slack_connection_dir,
                self.crm_binding, self.slack_binding, self.on_source_read)
            tls_directory = Path(self._stack.enter_context(tempfile.TemporaryDirectory(prefix="gtm-slack-check-")))

            def connector_factory(authority):
                return Connector(self.directory / "crm.sqlite3", self.directory / "calls.sqlite3", audit_path,
                                 authority, slack_reader=self.slack_reader, _slack_only=True, on_call=self.on_call)

            services = self._stack.enter_context(_LocalHTTPS(tls_directory, connector_factory))
            self.config, self.connector = services.config, services.connector
            self.config.update(crm_backend=self.crm_backend, slack_backend="slack")
            return self
        except BaseException as error:
            self._stack.close()
            if isinstance(error, sqlite3.Error):
                raise SlackMappingError("slack_mapping_invalid") from None
            if isinstance(error, OSError):
                raise SlackMappingError("slack_storage_failed") from None
            if isinstance(error, ValueError) and not isinstance(error, StateInUseError):
                raise SlackMappingError("slack_mapping_invalid") from None
            raise

    def __exit__(self, *_):
        self._stack.close()


class Runtime:
    """One process, two ports, four databases; owns shutdown and a state lock."""

    def __init__(self, directory, *, rate_limit=60, crm_reader=None, on_call=None,
                 calls_backend="fixture", calls_reader=None, slack_backend="fixture", slack_connection_dir=None,
                 slack_reader=None, on_source_read=None, slack_expected_binding=None, selection=None):
        if calls_backend not in ("fixture", "imported", "none"):
            raise ValueError("The calls backend must be fixture, imported, or none.")
        if "none" in (calls_backend, slack_backend) and crm_reader is None:
            raise ValueError("Only a connected CRM can have a source that is not connected.")
        if selection is not None:
            from .selection import Selection
            if not isinstance(selection, Selection):
                raise ValueError("A selection must come from load_selection.")
            if selection.crm_backend != ("sqlite" if crm_reader is None else crm_reader.name):
                raise SourceIdentityError("The selection was written for another CRM backend. "
                                          "Use acc-007 style IDs for fixtures or acc-sf- IDs for Salesforce.")
        self.selection = selection
        if calls_reader is not None and (calls_backend != "imported" or calls_reader.name != "imported"):
            raise ValueError("An imported calls reader requires imported calls.")
        if slack_backend not in ("fixture", "slack", "none"):
            raise ValueError("The Slack backend must be fixture, slack, or none.")
        if slack_backend == "slack" and slack_connection_dir is None:
            raise SourceIdentityError("Live Slack requires its private connection folder.")
        if slack_reader is not None and (slack_backend != "slack" or slack_reader.name != "slack"):
            raise SourceIdentityError("The Slack reader does not match the selected source mode.")
        if slack_expected_binding is not None:
            from .slack_mapping import valid_slack_binding
            if slack_backend != "slack" or not valid_slack_binding(slack_expected_binding):
                raise SourceIdentityError("The expected Slack identity does not match the selected source mode.")
        self.slack_expected_binding = deepcopy(slack_expected_binding)
        self.directory = _state_directory(directory)
        self.rate_limit = rate_limit
        self.crm_reader = crm_reader
        self.on_call = on_call
        self.crm_backend = "sqlite" if crm_reader is None else crm_reader.name
        self.crm_connection_identity = None
        self.calls_backend = calls_backend
        self.calls_reader = calls_reader
        self.calls_binding = None
        self.crm_binding = None
        self.slack_backend = slack_backend
        self.slack_connection_dir = (None if slack_connection_dir is None
                                     else Path(slack_connection_dir).expanduser().absolute())
        self.slack_reader = slack_reader
        self.slack_binding = None
        self.slack_mapping_path = self.directory / "slack-mappings.json"
        self.sellers: tuple = ()
        self.on_source_read = on_source_read
        self.imports_path = self.directory / "imports.sqlite3"
        self.servers, self.threads, self.sockets = [], [], []
        self.lock = None
        self._services = None

    def __enter__(self):
        try:
            if self.directory.is_symlink():
                raise SourceIdentityError("The state folder must not be a symbolic link. Use a private --state-dir.")
            if (self.calls_backend == "imported" or self.slack_backend == "slack") and self.directory.exists():
                try:
                    _private_existing(self.directory, directory=True)
                except (OSError, ValueError):
                    raise SourceIdentityError("Imported calls require a private state folder. "
                                              "Select a fresh --state-dir.") from None
            self.directory.mkdir(parents=True, mode=0o700, exist_ok=True)
            self.directory.chmod(0o700)
            descriptor = os.open(self.directory / "server.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
            self.lock = os.fdopen(descriptor, "wb")
            try:
                fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise StateInUseError("this state directory already has a running connector; "
                                      "stop it with Ctrl-C or choose another --state-dir") from exc
            self.sellers = _sellers(self.directory)
            slack_marker, self.slack_binding = _prepare_slack_selection(
                self.directory, self.slack_backend, self.slack_connection_dir, self.slack_expected_binding)
            self.crm_connection_identity = _prepare_source_state(self.directory, self.crm_reader,
                                                                  calls_backend=self.calls_backend)
            self.crm_binding = _calls_binding(self.crm_connection_identity)
            if self.slack_backend == "slack":
                _finish_slack_selection(self.directory, slack_marker, self.crm_binding, self.slack_binding)
                self.slack_reader = _selected_slack_reader(
                    self.slack_reader, self.slack_mapping_path, self.slack_connection_dir,
                    self.crm_binding, self.slack_binding, self.on_source_read)
            require_distinct_databases([self.directory / f"{name}.sqlite3"
                                        for name in _DATABASE_NAMES])
            if self.calls_backend == "imported":
                from .calls_import import ImportedCallsReader, initialize_import_store
                self.calls_binding = self.crm_binding
                if self.calls_reader is not None:
                    try:
                        if (Path(self.calls_reader.path).resolve() != self.imports_path
                                or self.calls_reader.crm_binding != self.calls_binding):
                            raise ValueError
                    except (AttributeError, TypeError, ValueError):
                        raise SourceIdentityError("The imported calls reader does not match this private source. "
                                                  "Select the original connection and state folder.") from None
                initialize_import_store(self.imports_path, crm_binding=self.calls_binding)
                if self.calls_reader is None:
                    self.calls_reader = ImportedCallsReader(self.imports_path, crm_binding=self.calls_binding)
            for name, module in (("crm", store), ("calls", calls), ("slack", slack)):
                if name == "calls" and self.calls_backend != "fixture":
                    continue
                if name == "slack" and self.slack_backend != "fixture":
                    continue
                path = self.directory / f"{name}.sqlite3"
                if path.is_symlink():
                    raise ValueError("source databases must not be symbolic links")
                with closing(module.connect(path)) as connection:
                    module.initialize(connection)
                    module.seed(connection)
                path.chmod(0o600)
            calls_path = self.imports_path if self.calls_backend == "imported" else self.directory / "calls.sqlite3"
            selected = self._selected_context()

            def connector_factory(authority):
                return Connector(self.directory / "crm.sqlite3", calls_path,
                                 self.directory / "audit.sqlite3", authority, rate_limit=self.rate_limit,
                                 slack_path=self.directory / "slack.sqlite3", crm_reader=self.crm_reader,
                                 on_call=self.on_call, calls_reader=self.calls_reader, slack_reader=self.slack_reader,
                                 calls_connected=self.calls_backend != "none",
                                 slack_connected=self.slack_backend != "none",
                                 selected=selected)

            self._services = _LocalHTTPS(self.directory, connector_factory)
            self._services.__enter__()
            self.config, self.connector = self._services.config, self._services.connector
            self.servers, self.threads, self.sockets = (self._services.servers, self._services.threads,
                                                       self._services.sockets)
            self.config.update(crm_backend=self.crm_backend, calls_backend=self.calls_backend,
                               slack_backend=self.slack_backend)
            if self.slack_backend == "slack":
                self.config["slack_connection_dir"] = str(self.slack_connection_dir)
            if self.crm_connection_identity is not None:
                self.config["sf_environment"] = self.crm_connection_identity["environment"]
            private_file(self.directory / "owner.json", json.dumps(self.config).encode())
            return self
        except BaseException:
            self.__exit__(None, None, None)
            raise

    def _selected_context(self):
        """Bind the immutable selection to this state's sources, reading metadata only."""
        if self.selection is None:
            return None
        from .selected_briefs import SelectedContext
        if self.slack_backend == "slack":
            from .slack_mapping import SlackMappingError, load_slack_mapping
            for account_id in sorted(self.selection.slack_accounts):
                try:
                    load_slack_mapping(self.slack_mapping_path, account_id=account_id,
                                       crm_binding=self.crm_binding, slack_binding=self.slack_binding)
                except SlackMappingError:
                    raise SourceIdentityError(
                        "A selected Slack thread is not mapped in this state folder. Map it with "
                        "gtm-connector slack map, or set slack to false for that account.") from None
        return SelectedContext(
            selection=self.selection, state_directory=self.directory, crm_binding=deepcopy(self.crm_binding),
            calls_backend=self.calls_backend, calls_binding=deepcopy(self.calls_binding),
            slack_backend=self.slack_backend, slack_binding=deepcopy(self.slack_binding),
            slack_mapping_path=self.slack_mapping_path if self.slack_backend == "slack" else None,
            slack_connection_dir=self.slack_connection_dir if self.slack_backend == "slack" else None,
            sellers=self.sellers)

    def __exit__(self, *_):
        if self._services is not None:
            self._services.__exit__(None, None, None)
        if self.lock is not None:
            self.lock.close()

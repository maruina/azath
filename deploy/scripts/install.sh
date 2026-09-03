#!/bin/sh
#
# install.sh downloads and installs azath from GitHub releases.

set -eu

REPO="maruina/azath"
OS_RELEASE_FILE="/etc/os-release"
DSM_VERSION_FILE="/etc.defaults/VERSION"
TMPDIR=""
PLATFORM=""
ARCH=""
VERSION="${AZATH_VERSION:-latest}"
ARCHIVE_NAME=""
ARCHIVE_KIND=""
CANDIDATE_PATH=""
CANDIDATE_IDENTITY=""
DOWNLOAD_HTTP_STATUS=""
BINARY_DIR="/usr/local/bin"
BINARY_PATH="$BINARY_DIR/azath"
BINARY_STAGE=""
INSTALLED_IDENTITY=""
SERVICE_WAS_ACTIVE=0

cleanup() {
  if [ -n "$BINARY_STAGE" ]; then
    rm -f "$BINARY_STAGE"
  fi
  if [ -n "$TMPDIR" ]; then
    rm -rf "$TMPDIR"
  fi
}

on_signal() {
  cleanup
  exit 1
}

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    die "$1 is required but not installed"
  fi
}

read_assignment() {
  assignment_file=$1
  assignment_key=$2

  if [ ! -r "$assignment_file" ]; then
    return 1
  fi

  while IFS= read -r assignment_line || [ -n "$assignment_line" ]; do
    case "$assignment_line" in
      "$assignment_key"=*)
        assignment_value=${assignment_line#*=}
        case "$assignment_value" in
          \"*\") assignment_value=${assignment_value#\"}; assignment_value=${assignment_value%\"} ;;
          \'*\') assignment_value=${assignment_value#\'}; assignment_value=${assignment_value%\'} ;;
        esac
        printf '%s\n' "$assignment_value"
        return 0
        ;;
    esac
  done < "$assignment_file"

  return 1
}

validate_root() {
  if [ "$(id -u)" -ne 0 ]; then
    die "run this installer as root, for example: curl -fsSL https://github.com/${REPO}/releases/latest/download/install.sh | sh"
  fi
}

validate_version() {
  case "$VERSION" in
    latest)
      ;;
    v*)
      case "$VERSION" in
        *[!abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-]*)
          die "AZATH_VERSION must be latest or a safe v-prefixed tag"
          ;;
      esac
      ;;
    *)
      die "AZATH_VERSION must be latest or a safe v-prefixed tag"
      ;;
  esac
}

detect_platform() {
  os_id=$(read_assignment "$OS_RELEASE_FILE" ID || true)
  os_version=$(read_assignment "$OS_RELEASE_FILE" VERSION_ID || true)
  dsm_major=$(read_assignment "$DSM_VERSION_FILE" majorversion || true)
  debian_supported=0
  dsm_supported=0

  if [ "$os_id" = "debian" ] && [ "$os_version" = "13" ]; then
    debian_supported=1
  fi
  if [ "$dsm_major" = "7" ]; then
    dsm_supported=1
  fi

  if [ "$debian_supported" -eq 1 ] && [ "$dsm_supported" -eq 1 ]; then
    die "ambiguous platform markers; supported platforms are Debian 13 and Synology DSM 7"
  fi
  if [ "$debian_supported" -eq 1 ]; then
    PLATFORM="debian"
    return
  fi
  if [ "$dsm_supported" -eq 1 ]; then
    PLATFORM="dsm"
    return
  fi

  die "unsupported platform; supported platforms are Debian 13 and Synology DSM 7"
}

detect_architecture() {
  machine=$(uname -m)
  case "$machine" in
    x86_64) ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
    *) die "unsupported architecture: $machine (supported mappings: x86_64 to amd64, aarch64 to arm64)" ;;
  esac
}

require_dependencies() {
  for required_command in awk chmod chown cp curl grep gzip mkdir mktemp mv rm sha256sum tar tr wc; do
    require_command "$required_command"
  done
}

create_tempdir() {
  TMPDIR=$(mktemp -d) || die "failed to create a temporary directory"
  chmod 700 "$TMPDIR" || die "failed to secure the temporary directory"
}

release_base_url() {
  if [ "$VERSION" = "latest" ]; then
    printf 'https://github.com/%s/releases/latest/download\n' "$REPO"
  else
    printf 'https://github.com/%s/releases/download/%s\n' "$REPO" "$VERSION"
  fi
}

download_file() {
  download_url=$1
  download_destination=$2
  DOWNLOAD_HTTP_STATUS=""

  if DOWNLOAD_HTTP_STATUS=$(curl --fail --silent --show-error --location --output "$download_destination" --write-out '%{http_code}' "$download_url"); then
    [ "$DOWNLOAD_HTTP_STATUS" = "200" ] || return 1
    return 0
  fi

  return 1
}

download_required_file() {
  download_url=$1
  download_destination=$2
  if ! download_file "$download_url" "$download_destination"; then
    die "failed to download $download_url (HTTP ${DOWNLOAD_HTTP_STATUS:-unavailable})"
  fi
}

select_archive() {
  archive_base="azath_linux_${ARCH}.tar.gz"
  archive_url="$(release_base_url)/$archive_base"
  ARCHIVE_KIND="stable"
  ARCHIVE_NAME="$archive_base"

  if download_file "$archive_url" "$TMPDIR/$archive_base"; then
    return
  fi

  if [ "$VERSION" != "latest" ] && [ "$DOWNLOAD_HTTP_STATUS" = "404" ]; then
    legacy_version=${VERSION#v}
    ARCHIVE_NAME="azath_${legacy_version}_linux_${ARCH}.tar.gz"
    ARCHIVE_KIND="legacy"
    download_required_file "$(release_base_url)/$ARCHIVE_NAME" "$TMPDIR/$ARCHIVE_NAME"
    return
  fi

  die "failed to download $archive_url (HTTP ${DOWNLOAD_HTTP_STATUS:-unavailable})"
}

verify_archive_checksum() {
  checksum_matches=$(awk -v archive="$ARCHIVE_NAME" '$2 == archive || $2 == "*" archive { print }' "$TMPDIR/checksums.txt")
  if [ -z "$checksum_matches" ]; then
    die "expected exactly one checksum for $ARCHIVE_NAME"
  fi
  checksum_count=$(printf '%s\n' "$checksum_matches" | wc -l | tr -d ' ')

  if [ "$checksum_count" -ne 1 ]; then
    die "expected exactly one checksum for $ARCHIVE_NAME"
  fi

  printf '%s\n' "$checksum_matches" > "$TMPDIR/selected.checksum"
  if ! (cd "$TMPDIR" && sha256sum -c selected.checksum); then
    die "checksum verification failed for $ARCHIVE_NAME"
  fi
}

extract_candidate() {
  if [ "$PLATFORM" = "debian" ] && [ "$ARCHIVE_KIND" = "stable" ]; then
    if ! tar -xzf "$TMPDIR/$ARCHIVE_NAME" -C "$TMPDIR" azath deploy/systemd/azath.service; then
      die "failed to extract required Debian archive members from $ARCHIVE_NAME"
    fi
  elif ! tar -xzf "$TMPDIR/$ARCHIVE_NAME" -C "$TMPDIR" azath; then
    die "failed to extract azath from $ARCHIVE_NAME"
  fi

  CANDIDATE_PATH="$TMPDIR/azath"
  if [ ! -x "$CANDIDATE_PATH" ]; then
    die "extracted azath binary is not executable"
  fi
  if ! CANDIDATE_IDENTITY=$("$CANDIDATE_PATH" --version); then
    die "candidate azath --version failed"
  fi
}

download_and_verify() {
  release_base=$(release_base_url)
  printf 'Downloading checksums for %s...\n' "$VERSION" >&2
  download_required_file "$release_base/checksums.txt" "$TMPDIR/checksums.txt"
  printf 'Downloading azath for %s...\n' "$ARCH" >&2
  select_archive
  printf 'Verifying %s...\n' "$ARCHIVE_NAME" >&2
  verify_archive_checksum
  extract_candidate
}

installed_identity() {
  if [ -x "$BINARY_PATH" ] && INSTALLED_IDENTITY=$("$BINARY_PATH" --version); then
    return 0
  fi
  INSTALLED_IDENTITY=""
  return 1
}

reconcile_dsm() {
  for directory in /etc/azath /etc/azath/secrets; do
    if [ ! -e "$directory" ]; then
      mkdir -p "$directory" || die "failed to create $directory"
      chmod 700 "$directory" || die "failed to secure $directory"
      chown root:root "$directory" || die "failed to set ownership on $directory"
    fi
  done
}

reconcile_debian_account() {
  require_command id
  require_command chown

  if id -u azath >/dev/null 2>&1; then
    service_group=$(id -gn azath) || die "failed to determine azath primary group"
  else
    require_command getent
    require_command groupadd
    require_command useradd
    if ! getent group azath >/dev/null 2>&1; then
      groupadd --system azath || die "failed to create azath system group"
    fi
    useradd --system --gid azath --shell /usr/sbin/nologin --no-create-home azath || die "failed to create azath system user"
    service_group=azath
  fi

  if [ ! -e /etc/azath ]; then
    mkdir -p /etc/azath || die "failed to create /etc/azath"
    chmod 700 /etc/azath || die "failed to secure /etc/azath"
    chown "azath:$service_group" /etc/azath || die "failed to set /etc/azath ownership"
  fi
}

reconcile_debian_unit() {
  unit_source="$TMPDIR/deploy/systemd/azath.service"
  unit_dir="/usr/local/lib/systemd/system"
  unit_path="$unit_dir/azath.service"

  if [ "$ARCHIVE_KIND" = "legacy" ]; then
    if [ ! -e "$unit_path" ] && [ ! -e /etc/systemd/system/azath.service ]; then
      printf 'warning: %s predates managed systemd packaging; no unit was installed.\n' "$VERSION" >&2
    fi
    return
  fi

  require_command cmp
  require_command cp
  require_command mv
  require_command systemctl
  require_command chown
  if [ -e "$unit_path" ] && cmp -s "$unit_source" "$unit_path"; then
    return
  fi

  if [ ! -e "$unit_dir" ]; then
    mkdir -p "$unit_dir" || die "failed to create $unit_dir"
    chmod 755 "$unit_dir" || die "failed to set mode on $unit_dir"
    chown root:root "$unit_dir" || die "failed to set ownership on $unit_dir"
  fi
  unit_stage=$(mktemp "$unit_dir/.azath.service.XXXXXX") || die "failed to stage systemd unit"
  cp "$unit_source" "$unit_stage" || die "failed to stage systemd unit"
  chmod 644 "$unit_stage" || die "failed to set systemd unit mode"
  chown root:root "$unit_stage" || die "failed to set systemd unit ownership"
  mv "$unit_stage" "$unit_path" || die "failed to install systemd unit"
  if ! systemctl daemon-reload; then
    die "systemd daemon reload failed; binary activation did not begin"
  fi
}

capture_service_state() {
  service_status=0
  if systemctl is-active azath.service >/dev/null 2>&1; then
    SERVICE_WAS_ACTIVE=1
    return
  else
    service_status=$?
  fi
  case "$service_status" in
    3|4) SERVICE_WAS_ACTIVE=0 ;;
    *) die "could not determine azath.service state; binary was not replaced" ;;
  esac
}

replace_binary() {
  mkdir -p "$BINARY_DIR" || die "failed to create $BINARY_DIR"
  BINARY_STAGE=$(mktemp "$BINARY_DIR/.azath.XXXXXX") || die "failed to stage azath binary"
  cp "$CANDIDATE_PATH" "$BINARY_STAGE" || die "failed to stage azath binary"
  chmod 755 "$BINARY_STAGE" || die "failed to set azath binary mode"
  mv "$BINARY_STAGE" "$BINARY_PATH" || die "failed to replace azath binary"
  BINARY_STAGE=""
}

restart_active_service() {
  if [ "$PLATFORM" = "debian" ] && [ "$SERVICE_WAS_ACTIVE" -eq 1 ]; then
    if ! systemctl restart azath.service; then
      printf 'installation partially succeeded: installed %s; previous identity was %s\n' "$CANDIDATE_IDENTITY" "${INSTALLED_IDENTITY:-unknown}" >&2
      systemctl status azath.service --no-pager >&2 || true
      case "$INSTALLED_IDENTITY" in
        v*) printf 'rollback: curl -fsSL https://github.com/%s/releases/latest/download/install.sh | AZATH_VERSION=%s sh\n' "$REPO" "${INSTALLED_IDENTITY%% *}" >&2 ;;
      esac
      exit 1
    fi
  fi
}

main() {
  validate_root
  validate_version
  detect_platform
  detect_architecture
  require_dependencies
  create_tempdir
  printf 'Detected %s on %s; requested release %s.\n' "$PLATFORM" "$ARCH" "$VERSION" >&2
  download_and_verify

  changed_binary=1
  if installed_identity && [ "$INSTALLED_IDENTITY" = "$CANDIDATE_IDENTITY" ]; then
    changed_binary=0
    printf 'Requested identity is already installed: %s\n' "$CANDIDATE_IDENTITY" >&2
  fi

  if [ "$PLATFORM" = "debian" ]; then
    reconcile_debian_account
    reconcile_debian_unit
    if [ "$changed_binary" -eq 1 ]; then
      require_command systemctl
      capture_service_state
    fi
  else
    reconcile_dsm
  fi

  if [ "$changed_binary" -eq 1 ]; then
    replace_binary
    restart_active_service
  fi

  printf 'Installed azath identity: %s\n' "$CANDIDATE_IDENTITY" >&2
  if [ "$PLATFORM" = "dsm" ]; then
    printf 'Configure the client, sealed blobs, unseal helper, and Task Scheduler as described in docs/runbooks/synology.md.\n' >&2
  fi
}

trap cleanup EXIT
trap on_signal HUP INT TERM

main "$@"

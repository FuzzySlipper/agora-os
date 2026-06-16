#include "policy_cache.hpp"
#include "protocol.hpp"

#include <sys/eventfd.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <sys/un.h>
#include <unistd.h>
#include <wayland-server-core.h>
#include <wayland-server-protocol.h>

#include <array>
#include <atomic>
#include <chrono>
#include <cerrno>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <functional>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <unordered_map>
#include <vector>

#include <wayfire/core.hpp>
#include <wayfire/nonstd/wlroots-full.hpp>
#include <wayfire/option-wrapper.hpp>
#include <wayfire/plugin.hpp>
#include <wayfire/seat.hpp>
#include <wayfire/signal-definitions.hpp>
#include <wayfire/util/log.hpp>
#include <wayfire/view-helpers.hpp>
#include <wayfire/view.hpp>

namespace
{
std::string role_to_string(wf::view_role_t role)
{
    switch (role)
    {
      case wf::VIEW_ROLE_TOPLEVEL:
        return "toplevel";
      case wf::VIEW_ROLE_UNMANAGED:
        return "unmanaged";
      case wf::VIEW_ROLE_DESKTOP_ENVIRONMENT:
        return "desktop-environment";
      default:
        return "unknown";
    }
}

agora::protocol::client_identity_t extract_client_identity(wayfire_view view)
{
    agora::protocol::client_identity_t identity;
    if (!view)
    {
        return identity;
    }

    auto *client = view->get_client();
    if (!client)
    {
        return identity;
    }

    pid_t pid = -1;
    uid_t uid = 0;
    gid_t gid = 0;
    wl_client_get_credentials(client, &pid, &uid, &gid);
    identity.pid = pid;
    identity.uid = uid;
    identity.gid = gid;
    return identity;
}

bool verify_bridge_peer_identity(int fd)
{
    ucred cred{};
    socklen_t len = sizeof(cred);
    if (::getsockopt(fd, SOL_SOCKET, SO_PEERCRED, &cred, &len) < 0)
    {
        LOGW("agora-bridge: SO_PEERCRED failed: ", std::strerror(errno));
        return false;
    }

    if (cred.uid != 0)
    {
        LOGW("agora-bridge: rejecting non-root bridge peer uid=", cred.uid);
        return false;
    }

    return true;
}

agora::protocol::surface_snapshot_t snapshot_view(wayfire_view view)
{
    agora::protocol::surface_snapshot_t snapshot;
    if (!view)
    {
        return snapshot;
    }

    snapshot.wayfire_view_id = view->get_id();
    snapshot.id = "view-" + std::to_string(view->get_id());
    snapshot.app_id = view->get_app_id();
    snapshot.title = view->get_title();
    snapshot.role = role_to_string(view->role);
    return snapshot;
}

void append_u32_be(std::vector<uint8_t>& out, uint32_t value)
{
    out.push_back(static_cast<uint8_t>((value >> 24) & 0xff));
    out.push_back(static_cast<uint8_t>((value >> 16) & 0xff));
    out.push_back(static_cast<uint8_t>((value >> 8) & 0xff));
    out.push_back(static_cast<uint8_t>(value & 0xff));
}

uint32_t crc32_bytes(const uint8_t *data, size_t size)
{
    uint32_t crc = 0xffffffffu;
    for (size_t i = 0; i < size; ++i)
    {
        crc ^= data[i];
        for (int bit = 0; bit < 8; ++bit)
        {
            crc = (crc >> 1) ^ (0xedb88320u & (0u - (crc & 1u)));
        }
    }

    return crc ^ 0xffffffffu;
}

uint32_t adler32_bytes(const uint8_t *data, size_t size)
{
    uint32_t a = 1;
    uint32_t b = 0;
    for (size_t i = 0; i < size; ++i)
    {
        a = (a + data[i]) % 65521u;
        b = (b + a) % 65521u;
    }

    return (b << 16) | a;
}

void append_png_chunk(std::vector<uint8_t>& png, const char type[4], const std::vector<uint8_t>& data)
{
    append_u32_be(png, static_cast<uint32_t>(data.size()));
    size_t chunk_start = png.size();
    png.insert(png.end(), type, type + 4);
    png.insert(png.end(), data.begin(), data.end());
    append_u32_be(png, crc32_bytes(png.data() + chunk_start, png.size() - chunk_start));
}

std::vector<uint8_t> zlib_store_uncompressed(const std::vector<uint8_t>& data)
{
    std::vector<uint8_t> out;
    out.push_back(0x78);
    out.push_back(0x01);

    size_t offset = 0;
    while (offset < data.size())
    {
        size_t remaining = data.size() - offset;
        uint16_t block_len = static_cast<uint16_t>(std::min<size_t>(remaining, 65535));
        bool final_block = (offset + block_len) == data.size();
        out.push_back(final_block ? 0x01 : 0x00);
        out.push_back(static_cast<uint8_t>(block_len & 0xff));
        out.push_back(static_cast<uint8_t>((block_len >> 8) & 0xff));
        uint16_t nlen = static_cast<uint16_t>(~block_len);
        out.push_back(static_cast<uint8_t>(nlen & 0xff));
        out.push_back(static_cast<uint8_t>((nlen >> 8) & 0xff));
        out.insert(out.end(), data.begin() + offset, data.begin() + offset + block_len);
        offset += block_len;
    }

    append_u32_be(out, adler32_bytes(data.data(), data.size()));
    return out;
}

std::vector<uint8_t> encode_png_rgba(uint32_t width, uint32_t height, const std::vector<uint8_t>& rgba)
{
    std::vector<uint8_t> scanlines;
    scanlines.reserve(static_cast<size_t>(height) * (1 + static_cast<size_t>(width) * 4));
    for (uint32_t y = 0; y < height; ++y)
    {
        scanlines.push_back(0);
        auto row_start = rgba.begin() + (static_cast<size_t>(y) * width * 4);
        scanlines.insert(scanlines.end(), row_start, row_start + (static_cast<size_t>(width) * 4));
    }

    std::vector<uint8_t> png = {0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a};
    std::vector<uint8_t> ihdr;
    append_u32_be(ihdr, width);
    append_u32_be(ihdr, height);
    ihdr.push_back(8); // bit depth
    ihdr.push_back(6); // RGBA
    ihdr.push_back(0); // deflate
    ihdr.push_back(0); // filter
    ihdr.push_back(0); // no interlace
    append_png_chunk(png, "IHDR", ihdr);
    append_png_chunk(png, "IDAT", zlib_store_uncompressed(scanlines));
    append_png_chunk(png, "IEND", {});
    return png;
}

std::string base64_encode(const std::vector<uint8_t>& data)
{
    static constexpr char alphabet[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    std::string out;
    out.reserve(((data.size() + 2) / 3) * 4);
    for (size_t i = 0; i < data.size(); i += 3)
    {
        uint32_t value = static_cast<uint32_t>(data[i]) << 16;
        if (i + 1 < data.size())
        {
            value |= static_cast<uint32_t>(data[i + 1]) << 8;
        }
        if (i + 2 < data.size())
        {
            value |= static_cast<uint32_t>(data[i + 2]);
        }

        out.push_back(alphabet[(value >> 18) & 0x3f]);
        out.push_back(alphabet[(value >> 12) & 0x3f]);
        out.push_back((i + 1 < data.size()) ? alphabet[(value >> 6) & 0x3f] : '=');
        out.push_back((i + 2 < data.size()) ? alphabet[value & 0x3f] : '=');
    }

    return out;
}

struct capture_surface_pick_t
{
    wlr_surface *surface = nullptr;
};

void pick_capture_surface(wlr_surface *surface, int, int, void *data)
{
    auto *pick = static_cast<capture_surface_pick_t*>(data);
    if (pick->surface || !surface || !surface->current.buffer ||
        (surface->current.buffer_width <= 0) || (surface->current.buffer_height <= 0))
    {
        return;
    }

    pick->surface = surface;
}

wlr_surface *find_capture_surface(wayfire_view view)
{
    if (!view)
    {
        return nullptr;
    }

    wlr_surface *root = view->get_wlr_surface();
    if (!root)
    {
        return nullptr;
    }

    capture_surface_pick_t pick;
    if (root->current.buffer && (root->current.buffer_width > 0) && (root->current.buffer_height > 0))
    {
        pick.surface = root;
    } else
    {
        wlr_surface_for_each_surface(root, pick_capture_surface, &pick);
    }

    return pick.surface;
}

class bridge_client_t
{
  public:
    using message_handler_t = std::function<void(const agora::protocol::bridge_message_t&)>;
    using connected_handler_t = std::function<void()>;

    bridge_client_t(std::string socket_path, message_handler_t on_message, connected_handler_t on_connected) :
        socket_path_(std::move(socket_path)),
        on_message_(std::move(on_message)),
        on_connected_(std::move(on_connected))
    {}

    ~bridge_client_t()
    {
        stop();
    }

    void start()
    {
        if (running_.exchange(true))
        {
            return;
        }

        worker_ = std::thread([this]
        {
            run();
        });
    }

    void stop()
    {
        if (!running_.exchange(false))
        {
            return;
        }

        close_fd();
        if (worker_.joinable())
        {
            worker_.join();
        }
    }

    void send_line(const std::string& line)
    {
        std::lock_guard lock(write_mutex_);
        int fd = fd_.load();
        if (fd < 0)
        {
            return;
        }

        const std::string framed = line + "\n";
        ssize_t written = ::send(fd, framed.data(), framed.size(), MSG_NOSIGNAL);
        if (written < 0)
        {
            LOGW("agora-bridge: send failed: ", std::strerror(errno));
            close_fd_locked();
        }
    }

  private:
    void run()
    {
        while (running_)
        {
            if (!ensure_connected())
            {
                sleep_for_retry();
                continue;
            }

            if (!read_loop())
            {
                close_fd();
                sleep_for_retry();
            }
        }
    }

    bool ensure_connected()
    {
        if (fd_.load() >= 0)
        {
            return true;
        }

        int fd = ::socket(AF_UNIX, SOCK_STREAM, 0);
        if (fd < 0)
        {
            LOGW("agora-bridge: socket() failed: ", std::strerror(errno));
            return false;
        }

        sockaddr_un addr{};
        addr.sun_family = AF_UNIX;
        std::snprintf(addr.sun_path, sizeof(addr.sun_path), "%s", socket_path_.c_str());
        if (::connect(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0)
        {
            ::close(fd);
            return false;
        }

        if (!verify_bridge_peer_identity(fd))
        {
            ::close(fd);
            return false;
        }

        fd_.store(fd);
        LOGI("agora-bridge: connected to ", socket_path_);
        if (on_connected_)
        {
            on_connected_();
        }

        return true;
    }

    bool read_loop()
    {
        std::string buffer;
        char chunk[4096];
        while (running_ && (fd_.load() >= 0))
        {
            int fd = fd_.load();
            ssize_t nread = ::recv(fd, chunk, sizeof(chunk), 0);
            if (nread == 0)
            {
                return false;
            }

            if (nread < 0)
            {
                if (errno == EINTR)
                {
                    continue;
                }

                return false;
            }

            buffer.append(chunk, chunk + nread);
            size_t newline = std::string::npos;
            while ((newline = buffer.find('\n')) != std::string::npos)
            {
                std::string line = buffer.substr(0, newline);
                buffer.erase(0, newline + 1);
                auto message = agora::protocol::parse_bridge_message(line);
                if (message.kind != agora::protocol::bridge_message_kind::invalid)
                {
                    on_message_(message);
                }
            }
        }

        return false;
    }

    void close_fd_locked()
    {
        int fd = fd_.exchange(-1);
        if (fd >= 0)
        {
            ::close(fd);
        }
    }

    void close_fd()
    {
        std::lock_guard lock(write_mutex_);
        close_fd_locked();
    }

    static void sleep_for_retry()
    {
        std::this_thread::sleep_for(std::chrono::milliseconds(500));
    }

    std::string socket_path_;
    message_handler_t on_message_;
    connected_handler_t on_connected_;
    std::atomic<bool> running_{false};
    std::thread worker_;
    std::mutex write_mutex_;
    std::atomic<int> fd_{-1};
};
} // namespace

class agora_bridge_plugin_t : public wf::plugin_interface_t
{
  public:
    void init() override
    {
        setup_close_wake();

        bridge_ = std::make_unique<bridge_client_t>(socket_path_.value(),
            [this] (const agora::protocol::bridge_message_t& message)
        {
            handle_bridge_message(message);
        },
            [this]
        {
            queue_surface_resync();
        });
        bridge_->start();

        wf::get_core().connect(&on_view_mapped_);
        wf::get_core().connect(&on_view_unmapped_);
        wf::get_core().connect(&on_keyboard_focus_changed_);
        wf::get_core().connect(&on_pointer_focus_changed_);
        wf::get_core().connect(&on_keyboard_key_);
        wf::get_core().connect(&on_pointer_button_);
    }

    void fini() override
    {
        on_view_mapped_.disconnect();
        on_view_unmapped_.disconnect();
        on_keyboard_focus_changed_.disconnect();
        on_pointer_focus_changed_.disconnect();
        on_keyboard_key_.disconnect();
        on_pointer_button_.disconnect();

        if (bridge_)
        {
            bridge_->stop();
            bridge_.reset();
        }

        teardown_close_wake();
    }

  private:
    struct pending_capture_request_t
    {
        std::string request_id;
        std::string surface_id;
    };

    static int handle_close_wake(int fd, uint32_t mask, void *data)
    {
        return static_cast<agora_bridge_plugin_t*>(data)->process_close_wake(fd, mask);
    }

    bool setup_close_wake()
    {
        close_wake_fd_ = eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
        if (close_wake_fd_ < 0)
        {
            LOGW("agora-bridge: eventfd() failed: ", std::strerror(errno));
            return false;
        }

        close_wake_source_ = wl_event_loop_add_fd(
            wf::get_core().ev_loop,
            close_wake_fd_,
            WL_EVENT_READABLE,
            handle_close_wake,
            this);
        if (!close_wake_source_)
        {
            LOGW("agora-bridge: wl_event_loop_add_fd() failed");
            ::close(close_wake_fd_);
            close_wake_fd_ = -1;
            return false;
        }

        return true;
    }

    void teardown_close_wake()
    {
        if (close_wake_source_)
        {
            wl_event_source_remove(close_wake_source_);
            close_wake_source_ = nullptr;
        }

        if (close_wake_fd_ >= 0)
        {
            ::close(close_wake_fd_);
            close_wake_fd_ = -1;
        }

        std::lock_guard lock(state_mutex_);
        views_by_surface_.clear();
        pending_close_surfaces_.clear();
        pending_close_owner_uids_.clear();
        pending_capture_requests_.clear();
        pending_surface_resync_ = false;
    }

    int process_close_wake(int fd, uint32_t mask)
    {
        if (mask & (WL_EVENT_ERROR | WL_EVENT_HANGUP))
        {
            return 0;
        }

        if ((mask & WL_EVENT_READABLE) == 0)
        {
            return 0;
        }

        uint64_t count = 0;
        while (::read(fd, &count, sizeof(count)) > 0)
        {}

        if ((errno != 0) && (errno != EAGAIN) && (errno != EWOULDBLOCK))
        {
            LOGW("agora-bridge: wake read failed: ", std::strerror(errno));
        }

        std::vector<std::string> close_surfaces;
        std::vector<uint32_t> close_owner_uids;
        std::vector<pending_capture_request_t> capture_requests;
        bool should_resync = false;
        std::unordered_map<std::string, wayfire_view> views;
        {
            std::lock_guard lock(state_mutex_);
            close_surfaces.swap(pending_close_surfaces_);
            close_owner_uids.swap(pending_close_owner_uids_);
            capture_requests.swap(pending_capture_requests_);
            should_resync = pending_surface_resync_;
            pending_surface_resync_ = false;
            views = views_by_surface_;
        }

        for (const auto& request : capture_requests)
        {
            process_capture_request(request, views);
        }

        if (should_resync)
        {
            resync_surfaces();
        }

        std::unordered_map<std::string, wayfire_view> targets;
        for (const auto& surface_id : close_surfaces)
        {
            auto it = views.find(surface_id);
            if ((it != views.end()) && it->second)
            {
                targets[surface_id] = it->second;
            }
        }

        for (auto owner_uid : close_owner_uids)
        {
            for (const auto& [surface_id, view] : views)
            {
                if (!view)
                {
                    continue;
                }

                auto identity = extract_client_identity(view);
                if (identity.uid == owner_uid)
                {
                    targets[surface_id] = view;
                }
            }
        }

        for (const auto& [surface_id, view] : targets)
        {
            if (!view)
            {
                continue;
            }

            LOGI("agora-bridge: closing surface ", surface_id);
            view->close();
        }

        return 0;
    }

    void queue_close_surface(std::string surface_id)
    {
        if (surface_id.empty())
        {
            return;
        }

        {
            std::lock_guard lock(state_mutex_);
            pending_close_surfaces_.push_back(std::move(surface_id));
        }

        notify_close_wake();
    }

    void queue_close_surfaces_by_uid(uint32_t owner_uid)
    {
        {
            std::lock_guard lock(state_mutex_);
            pending_close_owner_uids_.push_back(owner_uid);
        }

        notify_close_wake();
    }

    void queue_capture_surface(std::string request_id, std::string surface_id)
    {
        if (request_id.empty())
        {
            request_id = surface_id;
        }

        {
            std::lock_guard lock(state_mutex_);
            pending_capture_requests_.push_back({std::move(request_id), std::move(surface_id)});
        }

        notify_close_wake();
    }

    void send_capture_error(const pending_capture_request_t& request, std::string_view error)
    {
        if (!bridge_)
        {
            return;
        }

        bridge_->send_line(agora::protocol::encode_capture_response(
            request.request_id, request.surface_id, false, 0, 0, "png", "", error));
    }

    void process_capture_request(const pending_capture_request_t& request,
        const std::unordered_map<std::string, wayfire_view>& views)
    {
        if (!bridge_)
        {
            return;
        }

        auto view_it = views.find(request.surface_id);
        if ((view_it == views.end()) || !view_it->second)
        {
            send_capture_error(request, "surface not found");
            return;
        }

        auto view = view_it->second;
        wlr_surface *surface = find_capture_surface(view);
        if (!surface)
        {
            send_capture_error(request, "surface buffer not available");
            return;
        }

        uint32_t width = static_cast<uint32_t>(surface->current.buffer_width);
        uint32_t height = static_cast<uint32_t>(surface->current.buffer_height);
        if ((width == 0) || (height == 0))
        {
            send_capture_error(request, "surface buffer has empty dimensions");
            return;
        }

        void *data = nullptr;
        uint32_t format = 0;
        size_t stride = 0;
        if (!wlr_buffer_begin_data_ptr_access(surface->current.buffer,
            WLR_BUFFER_DATA_PTR_ACCESS_READ, &data, &format, &stride))
        {
            send_capture_error(request, "surface buffer does not support CPU readback");
            return;
        }

        std::vector<uint8_t> rgba(static_cast<size_t>(width) * height * 4);
        bool supported_format = (format == WL_SHM_FORMAT_ARGB8888) || (format == WL_SHM_FORMAT_XRGB8888);
        if (supported_format)
        {
            const auto *bytes = static_cast<const uint8_t*>(data);
            for (uint32_t y = 0; y < height; ++y)
            {
                const uint8_t *src = bytes + (static_cast<size_t>(y) * stride);
                uint8_t *dst = rgba.data() + (static_cast<size_t>(y) * width * 4);
                for (uint32_t x = 0; x < width; ++x)
                {
                    dst[x * 4 + 0] = src[x * 4 + 2];
                    dst[x * 4 + 1] = src[x * 4 + 1];
                    dst[x * 4 + 2] = src[x * 4 + 0];
                    dst[x * 4 + 3] = (format == WL_SHM_FORMAT_ARGB8888) ? src[x * 4 + 3] : 0xff;
                }
            }
        }
        wlr_buffer_end_data_ptr_access(surface->current.buffer);

        if (!supported_format)
        {
            send_capture_error(request, "unsupported shm buffer format");
            return;
        }

        auto png = encode_png_rgba(width, height, rgba);
        bridge_->send_line(agora::protocol::encode_capture_response(
            request.request_id, request.surface_id, true, width, height, "png", base64_encode(png)));
    }

    void queue_surface_resync()
    {
        {
            std::lock_guard lock(state_mutex_);
            pending_surface_resync_ = true;
        }

        notify_close_wake();
    }

    void resync_surfaces()
    {
        for (auto view : wf::get_core().get_all_views())
        {
            if (!view)
            {
                continue;
            }

            track_view(view);
            emit_surface_event("mapped", view);
        }
    }

    void notify_close_wake()
    {
        if (close_wake_fd_ < 0)
        {
            LOGW("agora-bridge: no wake fd available for close request");
            return;
        }

        uint64_t one = 1;
        if ((::write(close_wake_fd_, &one, sizeof(one)) < 0) && (errno != EAGAIN))
        {
            LOGW("agora-bridge: wake write failed: ", std::strerror(errno));
        }
    }

    void track_view(wayfire_view view)
    {
        if (!view)
        {
            return;
        }

        auto snapshot = snapshot_view(view);
        std::lock_guard lock(state_mutex_);
        views_by_surface_[snapshot.id] = view;
    }

    void forget_view(wayfire_view view)
    {
        if (!view)
        {
            return;
        }

        auto snapshot = snapshot_view(view);
        std::lock_guard lock(state_mutex_);
        views_by_surface_.erase(snapshot.id);
    }

    void handle_bridge_message(const agora::protocol::bridge_message_t& message)
    {
        switch (message.kind)
        {
          case agora::protocol::bridge_message_kind::policy_replace:
            policies_.replace(message.policies);
            break;
          case agora::protocol::bridge_message_kind::policy_upsert:
            if (!message.policies.empty())
            {
                policies_.upsert(message.policies.front());
            }
            break;
          case agora::protocol::bridge_message_kind::policy_remove:
            policies_.erase(message.surface_id);
            break;
          case agora::protocol::bridge_message_kind::input_context:
            policies_.set_actor_uid(message.actor_uid);
            break;
          case agora::protocol::bridge_message_kind::close_surface:
            queue_close_surface(message.surface_id);
            break;
          case agora::protocol::bridge_message_kind::close_surfaces_by_uid:
            if (message.owner_uid.has_value())
            {
                queue_close_surfaces_by_uid(*message.owner_uid);
            }
            break;
          case agora::protocol::bridge_message_kind::capture_surface:
            queue_capture_surface(message.request_id, message.surface_id);
            break;
          case agora::protocol::bridge_message_kind::invalid:
            break;
        }
    }

    void emit_surface_event(std::string_view event_name, wayfire_view view, std::string_view device = "")
    {
        if (!bridge_ || !view)
        {
            return;
        }

        auto snapshot = snapshot_view(view);
        auto identity = extract_client_identity(view);
        bridge_->send_line(agora::protocol::encode_surface_event(event_name, snapshot, identity, device));
    }

    bool should_allow(wayfire_view view, agora::input_device_t device) const
    {
        if (!view)
        {
            return true;
        }

        auto snapshot = snapshot_view(view);
        return policies_.allows(snapshot.id, device);
    }

    void maybe_report_input_denied(std::string_view device, wayfire_view view)
    {
        if (emit_input_denied_.value())
        {
            emit_surface_event("input_denied", view, device);
        }
    }

    wf::option_wrapper_t<std::string> socket_path_{"agora-bridge/socket_path"};
    wf::option_wrapper_t<bool> emit_input_denied_{"agora-bridge/emit_input_denied"};
    agora::policy_cache_t policies_;
    std::unique_ptr<bridge_client_t> bridge_;
    std::mutex state_mutex_;
    std::unordered_map<std::string, wayfire_view> views_by_surface_;
    std::vector<std::string> pending_close_surfaces_;
    std::vector<uint32_t> pending_close_owner_uids_;
    std::vector<pending_capture_request_t> pending_capture_requests_;
    bool pending_surface_resync_ = false;
    int close_wake_fd_ = -1;
    wl_event_source *close_wake_source_ = nullptr;
    wayfire_view keyboard_focus_view_;
    wayfire_view pointer_focus_view_;

    wf::signal::connection_t<wf::view_mapped_signal> on_view_mapped_ =
        [this] (wf::view_mapped_signal *ev)
    {
        track_view(ev->view);
        emit_surface_event("mapped", ev->view);
    };

    wf::signal::connection_t<wf::view_unmapped_signal> on_view_unmapped_ =
        [this] (wf::view_unmapped_signal *ev)
    {
        emit_surface_event("unmapped", ev->view);
        if (ev->view)
        {
            if (keyboard_focus_view_ == ev->view)
            {
                keyboard_focus_view_ = nullptr;
            }

            if (pointer_focus_view_ == ev->view)
            {
                pointer_focus_view_ = nullptr;
            }

            policies_.erase("view-" + std::to_string(ev->view->get_id()));
            forget_view(ev->view);
        }
    };

    wf::signal::connection_t<wf::keyboard_focus_changed_signal> on_keyboard_focus_changed_ =
        [this] (wf::keyboard_focus_changed_signal *ev)
    {
        keyboard_focus_view_ = wf::node_to_view(ev->new_focus);
        track_view(keyboard_focus_view_);
        if (keyboard_focus_view_)
        {
            emit_surface_event("focused", keyboard_focus_view_);
        }
    };

    wf::signal::connection_t<wf::pointer_focus_changed_signal> on_pointer_focus_changed_ =
        [this] (wf::pointer_focus_changed_signal *ev)
    {
        pointer_focus_view_ = wf::node_to_view(ev->new_focus);
        track_view(pointer_focus_view_);
    };

    wf::signal::connection_t<wf::input_event_signal<wlr_keyboard_key_event>> on_keyboard_key_ =
        [this] (wf::input_event_signal<wlr_keyboard_key_event> *ev)
    {
        if (!keyboard_focus_view_)
        {
            return;
        }

        if (!should_allow(keyboard_focus_view_, agora::input_device_t::keyboard))
        {
            ev->mode = wf::input_event_processing_mode_t::NO_CLIENT;
            maybe_report_input_denied("keyboard", keyboard_focus_view_);
        }
    };

    wf::signal::connection_t<wf::input_event_signal<wlr_pointer_button_event>> on_pointer_button_ =
        [this] (wf::input_event_signal<wlr_pointer_button_event> *ev)
    {
        if (!pointer_focus_view_)
        {
            return;
        }

        if (!should_allow(pointer_focus_view_, agora::input_device_t::pointer))
        {
            ev->mode = wf::input_event_processing_mode_t::NO_CLIENT;
            maybe_report_input_denied("pointer", pointer_focus_view_);
        }
    };
};

DECLARE_WAYFIRE_PLUGIN(agora_bridge_plugin_t);

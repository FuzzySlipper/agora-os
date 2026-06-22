#include "policy_cache.hpp"
#include "protocol.hpp"

#include <sys/eventfd.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <sys/un.h>
#include <unistd.h>
#include <wayland-server-core.h>
#include <wayland-server-protocol.h>

#include <algorithm>
#include <array>
#include <atomic>
#include <chrono>
#include <cerrno>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <sstream>
#include <string>
#include <thread>
#include <unordered_map>
#include <vector>

#include <wayfire/core.hpp>
#include <wayfire/nonstd/wlroots-full.hpp>
#include <wayfire/option-wrapper.hpp>
#include <wayfire/output.hpp>
#include <wayfire/opengl.hpp>
#include <wayfire/plugin.hpp>
#include <wayfire/plugins/wm-actions-signals.hpp>
#include <wayfire/seat.hpp>
#include <wayfire/signal-definitions.hpp>
#include <wayfire/util/log.hpp>
#include <wayfire/view-helpers.hpp>
#include <wayfire/workspace-set.hpp>
#include <wayfire/toplevel-view.hpp>
#include <wayfire/window-manager.hpp>
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
    if (view->role == wf::VIEW_ROLE_DESKTOP_ENVIRONMENT && snapshot.title == "layer-shell")
    {
        snapshot.surface_kind = "layer_shell";
        snapshot.id = "layer-shell-view-" + std::to_string(view->get_id());
        snapshot.role = "panel";
        snapshot.layer_namespace = "agora-webview";
        snapshot.layer_name = "top";
        snapshot.anchors = {"top"};
        snapshot.exclusive_zone = true;
    }
    if (auto *output = view->get_output())
    {
        if (output->handle && output->handle->name)
        {
            snapshot.output_id = output->handle->name;
        }
    }
    auto bbox = view->get_bounding_box();
    snapshot.x = bbox.x;
    snapshot.y = bbox.y;
    snapshot.width = bbox.width;
    snapshot.height = bbox.height;
    return snapshot;
}

std::string layer_name_from_value(uint32_t layer)
{
    switch (layer)
    {
      case 0:
        return "background";
      case 1:
        return "bottom";
      case 2:
        return "top";
      case 3:
        return "overlay";
      default:
        return "unknown";
    }
}

std::vector<std::string> anchors_from_mask(uint32_t anchor)
{
    std::vector<std::string> anchors;
    if (anchor & 1u)
    {
        anchors.push_back("top");
    }
    if (anchor & 2u)
    {
        anchors.push_back("bottom");
    }
    if (anchor & 4u)
    {
        anchors.push_back("left");
    }
    if (anchor & 8u)
    {
        anchors.push_back("right");
    }
    return anchors;
}

std::string role_from_layer_surface(const wlr_layer_surface_v1 *surface)
{
    if (!surface)
    {
        return "layer-shell";
    }
    const auto layer = static_cast<uint32_t>(surface->current.layer);
    const auto anchor = surface->current.anchor;
    if (layer == 3)
    {
        return "overlay";
    }
    if (layer == 0)
    {
        return "background";
    }
    if ((anchor & 4u) || (anchor & 8u))
    {
        return "dock";
    }
    return "panel";
}

std::string surface_id_for_layer_surface(const wlr_layer_surface_v1 *surface, const agora::protocol::client_identity_t& client)
{
    uint32_t resource_id = surface && surface->resource ? wl_resource_get_id(surface->resource) : 0;
    std::ostringstream out;
    out << "layer-shell-" << client.pid << "-" << resource_id;
    return out.str();
}

agora::protocol::surface_snapshot_t snapshot_layer_surface(wlr_layer_surface_v1 *surface,
    const agora::protocol::client_identity_t& client)
{
    agora::protocol::surface_snapshot_t snapshot;
    if (!surface)
    {
        return snapshot;
    }
    snapshot.id = surface_id_for_layer_surface(surface, client);
    snapshot.surface_kind = "layer_shell";
    snapshot.app_id = surface->namespace_t ? surface->namespace_t : "";
    snapshot.title = snapshot.app_id;
    snapshot.role = role_from_layer_surface(surface);
    snapshot.layer_namespace = surface->namespace_t ? surface->namespace_t : "";
    snapshot.layer_name = layer_name_from_value(static_cast<uint32_t>(surface->current.layer));
    snapshot.anchors = anchors_from_mask(surface->current.anchor);
    snapshot.exclusive_zone = surface->current.exclusive_zone;
    snapshot.width = static_cast<int32_t>(surface->current.actual_width ? surface->current.actual_width : surface->current.desired_width);
    snapshot.height = static_cast<int32_t>(surface->current.actual_height ? surface->current.actual_height : surface->current.desired_height);
    if (surface->surface)
    {
        snapshot.width = snapshot.width > 0 ? snapshot.width : surface->surface->current.width;
        snapshot.height = snapshot.height > 0 ? snapshot.height : surface->surface->current.height;
    }
    if (surface->output && surface->output->name)
    {
        snapshot.output_id = surface->output->name;
    }
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
        setup_layer_scan();
    }

    void fini() override
    {
        on_view_mapped_.disconnect();
        on_view_unmapped_.disconnect();
        on_keyboard_focus_changed_.disconnect();
        on_pointer_focus_changed_.disconnect();
        on_keyboard_key_.disconnect();
        on_pointer_button_.disconnect();
        teardown_layer_scan();

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

    struct pending_input_request_t
    {
        std::string request_id;
        std::string surface_id;
        std::string coordinate_space;
        std::vector<agora::protocol::input_event_t> events;
    };

    struct pending_place_request_t
    {
        std::string request_id;
        std::string surface_id;
        wf::geometry_t geometry;
    };

    struct pending_property_request_t
    {
        std::string request_id;
        std::string surface_id;
        std::optional<bool> always_on_top;
    };

    struct pending_surface_state_request_t
    {
        std::string request_id;
        std::string surface_id;
        std::optional<bool> fullscreen;
        std::optional<bool> maximized;
        std::optional<bool> minimized;
    };

    struct pending_focus_request_t
    {
        std::string request_id;
        std::string surface_id;
    };

    struct pending_raise_request_t
    {
        std::string request_id;
        std::string surface_id;
        std::string mode;
    };

    struct layer_surface_record_t
    {
        agora_bridge_plugin_t *owner = nullptr;
        wlr_layer_surface_v1 *surface = nullptr;
        agora::protocol::client_identity_t client;
        std::string surface_id;
        wl_listener map_listener{};
        wl_listener unmap_listener{};
        wl_listener commit_listener{};
        wl_listener destroy_listener{};
        bool mapped_sent = false;
    };

    static int handle_layer_scan(void *data)
    {
        auto *plugin = static_cast<agora_bridge_plugin_t*>(data);
        plugin->scan_layer_surfaces();
        if (plugin->layer_scan_source_)
        {
            wl_event_source_timer_update(plugin->layer_scan_source_, 250);
        }
        return 0;
    }

    static enum wl_iterator_result scan_layer_resource(struct wl_resource *resource, void *data)
    {
        auto *plugin = static_cast<agora_bridge_plugin_t*>(data);
        if (!resource)
        {
            return WL_ITERATOR_CONTINUE;
        }
        const char *resource_class = wl_resource_get_class(resource);
        if (resource_class && std::strcmp(resource_class, "zwlr_layer_surface_v1") == 0)
        {
            auto *layer = wlr_layer_surface_v1_from_resource(resource);
            if (layer)
            {
                plugin->track_layer_surface(layer);
            }
            return WL_ITERATOR_CONTINUE;
        }
        if (!resource_class || std::strcmp(resource_class, "wl_surface") != 0)
        {
            return WL_ITERATOR_CONTINUE;
        }
        auto *surface = wlr_surface_from_resource(resource);
        if (!surface)
        {
            return WL_ITERATOR_CONTINUE;
        }
        auto *layer = wlr_layer_surface_v1_try_from_wlr_surface(surface);
        if (layer)
        {
            plugin->track_layer_surface(layer);
        }
        return WL_ITERATOR_CONTINUE;
    }

    static void handle_layer_map(struct wl_listener *listener, void *data)
    {
        (void)data;
        layer_surface_record_t *record;
        record = wl_container_of(listener, record, map_listener);
        record->owner->send_layer_surface_mapped(record);
    }

    static void handle_layer_commit(struct wl_listener *listener, void *data)
    {
        (void)data;
        layer_surface_record_t *record;
        record = wl_container_of(listener, record, commit_listener);
        record->owner->send_layer_surface_mapped(record);
    }

    static void handle_layer_unmap(struct wl_listener *listener, void *data)
    {
        (void)data;
        layer_surface_record_t *record;
        record = wl_container_of(listener, record, unmap_listener);
        record->mapped_sent = false;
        record->owner->send_layer_surface_event(record, "unmapped");
    }

    static void handle_layer_destroy(struct wl_listener *listener, void *data)
    {
        (void)data;
        layer_surface_record_t *record;
        record = wl_container_of(listener, record, destroy_listener);
        if (record->mapped_sent)
        {
            record->owner->send_layer_surface_event(record, "unmapped");
        }
        record->owner->forget_layer_surface(record->surface);
    }

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
        pending_input_requests_.clear();
        pending_place_requests_.clear();
        pending_property_requests_.clear();
            pending_surface_state_requests_.clear();
        pending_focus_requests_.clear();
        pending_surface_resync_ = false;
    }

    void setup_layer_scan()
    {
        layer_scan_source_ = wl_event_loop_add_timer(wf::get_core().ev_loop, handle_layer_scan, this);
        if (!layer_scan_source_)
        {
            LOGW("agora-bridge: layer-shell scan timer setup failed");
            return;
        }
        wl_event_source_timer_update(layer_scan_source_, 1);
    }

    void teardown_layer_scan()
    {
        if (layer_scan_source_)
        {
            wl_event_source_remove(layer_scan_source_);
            layer_scan_source_ = nullptr;
        }
        for (auto& item : layer_surfaces_)
        {
            wl_list_remove(&item.second->map_listener.link);
            wl_list_remove(&item.second->unmap_listener.link);
            wl_list_remove(&item.second->commit_listener.link);
            wl_list_remove(&item.second->destroy_listener.link);
        }
        layer_surfaces_.clear();
    }

    void scan_layer_surfaces()
    {
        wl_client *client = nullptr;
        wl_client_for_each(client, wl_display_get_client_list(wf::get_core().display))
        {
            wl_client_for_each_resource(client, scan_layer_resource, this);
        }
    }

    void track_layer_surface(wlr_layer_surface_v1 *surface)
    {
        if (!surface || layer_surfaces_.count(surface) != 0 || !surface->surface)
        {
            return;
        }
        auto record = std::make_unique<layer_surface_record_t>();
        record->owner = this;
        record->surface = surface;
        if (surface->surface->resource)
        {
            auto *client = wl_resource_get_client(surface->surface->resource);
            if (client)
            {
                pid_t pid = -1;
                uid_t uid = 0;
                gid_t gid = 0;
                wl_client_get_credentials(client, &pid, &uid, &gid);
                record->client.pid = pid;
                record->client.uid = uid;
                record->client.gid = gid;
            }
        }
        record->surface_id = surface_id_for_layer_surface(surface, record->client);
        record->map_listener.notify = handle_layer_map;
        record->unmap_listener.notify = handle_layer_unmap;
        record->commit_listener.notify = handle_layer_commit;
        record->destroy_listener.notify = handle_layer_destroy;
        wl_signal_add(&surface->surface->events.map, &record->map_listener);
        wl_signal_add(&surface->surface->events.unmap, &record->unmap_listener);
        wl_signal_add(&surface->surface->events.client_commit, &record->commit_listener);
        wl_signal_add(&surface->events.destroy, &record->destroy_listener);
        auto *raw_record = record.get();
        layer_surfaces_[surface] = std::move(record);
        send_layer_surface_mapped(raw_record);
    }

    void forget_layer_surface(wlr_layer_surface_v1 *surface)
    {
        auto iter = layer_surfaces_.find(surface);
        if (iter == layer_surfaces_.end())
        {
            return;
        }
        wl_list_remove(&iter->second->map_listener.link);
        wl_list_remove(&iter->second->unmap_listener.link);
        wl_list_remove(&iter->second->commit_listener.link);
        wl_list_remove(&iter->second->destroy_listener.link);
        layer_surfaces_.erase(iter);
    }

    void send_layer_surface_mapped(layer_surface_record_t *record)
    {
        if (!record || !record->surface || !record->surface->surface || record->mapped_sent)
        {
            return;
        }
        record->mapped_sent = true;
        send_layer_surface_event(record, "mapped");
    }

    void send_layer_surface_event(layer_surface_record_t *record, std::string_view event)
    {
        if (!record || !record->surface || !bridge_)
        {
            return;
        }
        auto snapshot = snapshot_layer_surface(record->surface, record->client);
        if (snapshot.id.empty())
        {
            snapshot.id = record->surface_id;
        }
        bridge_->send_line(agora::protocol::encode_surface_event(event, snapshot, record->client));
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
        std::vector<pending_input_request_t> input_requests;
        std::vector<pending_place_request_t> place_requests;
        std::vector<pending_property_request_t> property_requests;
        std::vector<pending_surface_state_request_t> state_requests;
        std::vector<pending_focus_request_t> focus_requests;
        std::vector<pending_raise_request_t> raise_requests;
        bool should_resync = false;
        std::unordered_map<std::string, wayfire_view> views;
        {
            std::lock_guard lock(state_mutex_);
            close_surfaces.swap(pending_close_surfaces_);
            close_owner_uids.swap(pending_close_owner_uids_);
            capture_requests.swap(pending_capture_requests_);
            input_requests.swap(pending_input_requests_);
            place_requests.swap(pending_place_requests_);
            property_requests.swap(pending_property_requests_);
            state_requests.swap(pending_surface_state_requests_);
            focus_requests.swap(pending_focus_requests_);
            raise_requests.swap(pending_raise_requests_);
            should_resync = pending_surface_resync_;
            pending_surface_resync_ = false;
            views = views_by_surface_;
        }

        for (const auto& request : place_requests)
        {
            process_place_request(request, views);
        }

        for (const auto& request : property_requests)
        {
            process_property_request(request, views);
        }
        for (const auto& request : state_requests)
        {
            process_surface_state_request(request, views);
        }
        for (const auto& request : focus_requests)
        {
            process_focus_request(request, views);
        }
        for (const auto& request : raise_requests)
        {
            process_raise_request(request, views);
        }

        for (const auto& request : capture_requests)
        {
            process_capture_request(request, views);
        }

        for (const auto& request : input_requests)
        {
            process_input_request(request, views);
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

    void queue_input_request(std::string request_id, std::string surface_id, std::string coordinate_space,
        std::vector<agora::protocol::input_event_t> events)
    {
        if (request_id.empty())
        {
            request_id = surface_id;
        }

        {
            std::lock_guard lock(state_mutex_);
            pending_input_requests_.push_back({std::move(request_id), std::move(surface_id),
                std::move(coordinate_space), std::move(events)});
        }

        notify_close_wake();
    }



    void queue_place_surface(std::string request_id, std::string surface_id, wf::geometry_t geometry)
    {
        if (request_id.empty())
        {
            request_id = surface_id;
        }
        if (surface_id.empty() || geometry.width <= 0 || geometry.height <= 0)
        {
            return;
        }

        {
            std::lock_guard lock(state_mutex_);
            pending_place_requests_.push_back({std::move(request_id), std::move(surface_id), geometry});
        }

        notify_close_wake();
    }

    void queue_focus_surface(std::string request_id, std::string surface_id)
    {
        if (request_id.empty())
        {
            request_id = surface_id;
        }
        if (surface_id.empty())
        {
            return;
        }

        {
            std::lock_guard lock(state_mutex_);
            pending_focus_requests_.push_back({std::move(request_id), std::move(surface_id)});
        }

        notify_close_wake();
    }

    void queue_raise_surface(std::string request_id, std::string surface_id, std::string mode)
    {
        if (request_id.empty())
        {
            request_id = surface_id;
        }
        if (surface_id.empty())
        {
            return;
        }
        if (mode.empty())
        {
            mode = "no-focus";
        }

        {
            std::lock_guard lock(state_mutex_);
            pending_raise_requests_.push_back({std::move(request_id), std::move(surface_id), std::move(mode)});
        }

        notify_close_wake();
    }

    void queue_property_request(std::string request_id, std::string surface_id, std::optional<bool> always_on_top)
    {
        if (request_id.empty())
        {
            request_id = surface_id;
        }
        if (surface_id.empty() || !always_on_top.has_value())
        {
            return;
        }

        {
            std::lock_guard lock(state_mutex_);
            pending_property_requests_.push_back({std::move(request_id), std::move(surface_id), always_on_top});
        }

        notify_close_wake();
    }

    void queue_surface_state_request(std::string request_id, std::string surface_id, std::optional<bool> fullscreen,
        std::optional<bool> maximized, std::optional<bool> minimized)
    {
        if (request_id.empty())
        {
            request_id = surface_id;
        }
        if (surface_id.empty())
        {
            return;
        }

        {
            std::lock_guard lock(state_mutex_);
            pending_surface_state_requests_.push_back({std::move(request_id), std::move(surface_id), fullscreen, maximized, minimized});
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

    uint32_t now_msec() const
    {
        auto now = std::chrono::steady_clock::now().time_since_epoch();
        return static_cast<uint32_t>(
            std::chrono::duration_cast<std::chrono::milliseconds>(now).count() & 0xffffffffu);
    }

    double clamp_coordinate(double value, int32_t max_value) const
    {
        if (max_value <= 0)
        {
            return 0;
        }

        return std::clamp(value, 0.0, static_cast<double>(max_value - 1));
    }

    wlr_surface *input_surface_for_view(wayfire_view view) const
    {
        if (!view)
        {
            return nullptr;
        }

        if (auto *surface = view->get_keyboard_focus_surface())
        {
            return surface;
        }

        return view->get_wlr_surface();
    }

    void send_input_response(const pending_input_request_t& request, bool ok, uint32_t accepted,
        uint32_t rejected, std::string_view error = "")
    {
        if (!bridge_)
        {
            return;
        }

        bridge_->send_line(agora::protocol::encode_input_response(
            request.request_id, request.surface_id, ok, accepted, rejected, error));
    }

    bool apply_input_event(const agora::protocol::input_event_t& event, wayfire_view view,
        wlr_surface *surface, wlr_seat *seat, const wf::geometry_t& bbox)
    {
        if (!seat)
        {
            return false;
        }

        auto time = now_msec();
        auto x = clamp_coordinate(event.x, bbox.width);
        auto y = clamp_coordinate(event.y, bbox.height);
        switch (event.kind)
        {
          case agora::protocol::input_event_kind::pointer_move:
            if (!surface)
            {
                return false;
            }
            wlr_seat_pointer_notify_enter(seat, surface, x, y);
            wlr_seat_pointer_notify_motion(seat, time, x, y);
            wlr_seat_pointer_notify_frame(seat);
            return true;

          case agora::protocol::input_event_kind::pointer_button:
            if (!surface || (event.button == 0))
            {
                return false;
            }
            wlr_seat_pointer_notify_enter(seat, surface, x, y);
            wlr_seat_pointer_notify_button(seat, time, event.button,
                event.state ? WL_POINTER_BUTTON_STATE_PRESSED : WL_POINTER_BUTTON_STATE_RELEASED);
            wlr_seat_pointer_notify_frame(seat);
            return true;

          case agora::protocol::input_event_kind::key:
            if (event.keycode == 0)
            {
                return false;
            }
            wf::get_core().seat->focus_view(view);
            wlr_seat_keyboard_notify_key(seat, time, event.keycode,
                event.state ? WL_KEYBOARD_KEY_STATE_PRESSED : WL_KEYBOARD_KEY_STATE_RELEASED);
            return true;

          case agora::protocol::input_event_kind::scroll:
            if (!surface)
            {
                return false;
            }
            wlr_seat_pointer_notify_enter(seat, surface, x, y);
            wlr_seat_pointer_notify_axis(seat, time,
                event.axis == 1 ? WL_POINTER_AXIS_HORIZONTAL_SCROLL : WL_POINTER_AXIS_VERTICAL_SCROLL,
                event.value, event.discrete, WL_POINTER_AXIS_SOURCE_WHEEL,
                WL_POINTER_AXIS_RELATIVE_DIRECTION_IDENTICAL);
            wlr_seat_pointer_notify_frame(seat);
            return true;

          case agora::protocol::input_event_kind::touch:
            if (!surface)
            {
                return false;
            }
            if ((event.phase == "down") || (event.state == 1))
            {
                return wlr_seat_touch_notify_down(seat, surface, time, event.touch_id, x, y) != 0;
            }
            if (event.phase == "motion")
            {
                wlr_seat_touch_notify_motion(seat, time, event.touch_id, x, y);
                return true;
            }
            if ((event.phase == "up") || (event.state == 0))
            {
                return wlr_seat_touch_notify_up(seat, time, event.touch_id) != 0;
            }
            return false;

          default:
            return false;
        }
    }

    bool supports_input_coordinate_space(std::string_view coordinate_space) const
    {
        return coordinate_space.empty() || (coordinate_space == "surface") ||
            (coordinate_space == "surface-local");
    }

    void process_input_request(const pending_input_request_t& request,
        const std::unordered_map<std::string, wayfire_view>& views)
    {
        if (!supports_input_coordinate_space(request.coordinate_space))
        {
            send_input_response(request, false, 0, static_cast<uint32_t>(request.events.size()),
                "unsupported coordinate_space");
            return;
        }

        auto view_it = views.find(request.surface_id);
        if ((view_it == views.end()) || !view_it->second)
        {
            send_input_response(request, false, 0, static_cast<uint32_t>(request.events.size()), "surface not found");
            return;
        }

        auto view = view_it->second;
        auto bbox = view->get_bounding_box();
        if ((bbox.width <= 0) || (bbox.height <= 0))
        {
            send_input_response(request, false, 0, static_cast<uint32_t>(request.events.size()),
                "surface has empty dimensions");
            return;
        }

        auto *surface = input_surface_for_view(view);
        auto *seat = wf::get_core().seat ? wf::get_core().seat->seat : nullptr;
        if (!seat)
        {
            send_input_response(request, false, 0, static_cast<uint32_t>(request.events.size()), "seat not available");
            return;
        }

        wf::get_core().seat->focus_view(view);
        uint32_t accepted = 0;
        uint32_t rejected = 0;
        for (const auto& event : request.events)
        {
            if (apply_input_event(event, view, surface, seat, bbox))
            {
                ++accepted;
            } else
            {
                ++rejected;
            }
        }

        send_input_response(request, rejected == 0, accepted, rejected,
            rejected == 0 ? "" : "one or more input events were rejected");
    }


    void process_place_request(const pending_place_request_t& request,
        const std::unordered_map<std::string, wayfire_view>& views)
    {
        auto it = views.find(request.surface_id);
        if ((it == views.end()) || !it->second)
        {
            LOGW("agora-bridge: place target not found: ", request.surface_id);
            send_place_response(request, false, "surface not found");
            return;
        }

        auto toplevel = dynamic_cast<wf::toplevel_view_interface_t*>(it->second.get());
        if (!toplevel)
        {
            LOGW("agora-bridge: place target is not a toplevel: ", request.surface_id);
            send_place_response(request, false, "surface is not a toplevel");
            return;
        }

        toplevel->set_geometry(request.geometry);
        track_view(it->second);
        emit_surface_event("focused", it->second);
        send_place_response(request, true, "");
    }

    bool set_view_always_on_top(std::string_view surface_id, wayfire_view view, bool above)
    {
        if (!view)
        {
            return false;
        }
        auto *output = view->get_output();
        if (!output)
        {
            return false;
        }
        {
            std::lock_guard lock(state_mutex_);
            auto existing = always_on_top_by_surface_.find(std::string{surface_id});
            if ((existing != always_on_top_by_surface_.end()) && (existing->second == above))
            {
                return true;
            }
        }
        bool observed = false;
        wf::signal::connection_t<wf::wm_actions_above_changed_signal> on_above_changed =
            [&] (wf::wm_actions_above_changed_signal *ev)
        {
            if (ev && (ev->view == view))
            {
                observed = true;
            }
        };
        output->connect(&on_above_changed);
        wf::wm_actions_set_above_state_signal signal{view, above};
        output->emit(&signal);
        on_above_changed.disconnect();
        if (observed)
        {
            std::lock_guard lock(state_mutex_);
            always_on_top_by_surface_[std::string{surface_id}] = above;
        }
        return observed;
    }


    std::string scene_layer_name(wf::scene::layer layer) const
    {
        switch (layer)
        {
          case wf::scene::layer::BACKGROUND: return "background";
          case wf::scene::layer::BOTTOM: return "bottom";
          case wf::scene::layer::WORKSPACE: return "workspace";
          case wf::scene::layer::TOP: return "top";
          case wf::scene::layer::UNMANAGED: return "unmanaged";
          case wf::scene::layer::OVERLAY: return "overlay";
          case wf::scene::layer::LOCK: return "lock";
          case wf::scene::layer::DWIDGET: return "dwidget";
          default: return "unknown";
        }
    }

    void annotate_stack_readback(agora::protocol::surface_snapshot_t& snapshot, wayfire_view view)
    {
        if (!view)
        {
            return;
        }
        auto *output = view->get_output();
        if (!output)
        {
            return;
        }
        if (snapshot.output_id.empty() && output->handle && output->handle->name)
        {
            snapshot.output_id = output->handle->name;
        }
        auto layer = wf::get_view_layer(view);
        if (!layer.has_value())
        {
            return;
        }
        snapshot.stack_layer = scene_layer_name(*layer);
        if (auto wset = output->wset())
        {
            auto ws = wset->get_current_workspace();
            snapshot.workspace_x = ws.x;
            snapshot.workspace_y = ws.y;
        }
        auto stack = wf::collect_views_from_output(output, {*layer});
        auto it = std::find(stack.begin(), stack.end(), view);
        if (it == stack.end())
        {
            return;
        }
        int count = static_cast<int>(stack.size());
        int front_index = static_cast<int>(std::distance(stack.begin(), it));
        int bottom_to_top_index = count - 1 - front_index;
        snapshot.stack_count = count;
        snapshot.stack_index = bottom_to_top_index;
        snapshot.is_top_in_stack = (front_index == 0);
        snapshot.z_order_generation = z_order_generation_;
    }

    void send_raise_response(const pending_raise_request_t& request, bool ok, std::string_view error)
    {
        if (!bridge_)
        {
            return;
        }
        bridge_->send_line(agora::protocol::encode_raise_response(request.request_id, request.surface_id, ok, error));
    }

    void process_raise_request(const pending_raise_request_t& request,
        const std::unordered_map<std::string, wayfire_view>& views)
    {
        if (!request.mode.empty() && (request.mode != "no-focus"))
        {
            send_raise_response(request, false, "unsupported raise mode");
            return;
        }
        auto it = views.find(request.surface_id);
        if ((it == views.end()) || !it->second)
        {
            LOGW("agora-bridge: raise target not found: ", request.surface_id);
            send_raise_response(request, false, "surface not found");
            return;
        }
        auto view = it->second;
        auto toplevel = dynamic_cast<wf::toplevel_view_interface_t*>(view.get());
        if (!toplevel)
        {
            send_raise_response(request, false, "surface is not a toplevel");
            return;
        }
        auto focus_before = keyboard_focus_view_;
        wf::view_bring_to_front(view);
        z_order_generation_++;
        resync_surfaces();
        emit_surface_event("stacked", view);
        if (keyboard_focus_view_ != focus_before)
        {
            send_raise_response(request, false, "raise changed keyboard focus");
            return;
        }
        auto snapshot = snapshot_view_with_state(view);
        if (!snapshot.is_top_in_stack.has_value() || !*snapshot.is_top_in_stack)
        {
            send_raise_response(request, false, "raise did not make target top in scoped stack");
            return;
        }
        send_raise_response(request, true, "");
    }

    void send_surface_state_response(const pending_surface_state_request_t& request, bool ok, std::string_view error)
    {
        if (!bridge_)
        {
            return;
        }
        bridge_->send_line(agora::protocol::encode_surface_state_response(request.request_id, request.surface_id, ok, error));
    }

    bool set_view_fullscreen(wayfire_view view, wf::toplevel_view_interface_t *toplevel, bool fullscreen)
    {
        if (!view || !toplevel)
        {
            return false;
        }
        auto *output = view->get_output();
        if (!output || !wf::get_core().default_wm)
        {
            return false;
        }
        if (toplevel->pending_fullscreen() == fullscreen)
        {
            return true;
        }
        bool observed = false;
        wf::signal::connection_t<wf::view_fullscreen_signal> on_fullscreen = [&] (wf::view_fullscreen_signal *ev)
        {
            if (ev && (ev->view.get() == toplevel) && (ev->state == fullscreen))
            {
                observed = true;
            }
        };
        view->connect(&on_fullscreen);
        wf::get_core().default_wm->fullscreen_request(wayfire_toplevel_view{toplevel}, output, fullscreen);
        on_fullscreen.disconnect();
        return observed || (toplevel->pending_fullscreen() == fullscreen);
    }

    bool set_view_maximized(wayfire_view view, wf::toplevel_view_interface_t *toplevel, bool maximized)
    {
        if (!view || !toplevel || !wf::get_core().default_wm)
        {
            return false;
        }
        const uint32_t target_edges = maximized ? wf::TILED_EDGES_ALL : 0;
        if (toplevel->pending_tiled_edges() == target_edges)
        {
            return true;
        }
        bool observed = false;
        wf::signal::connection_t<wf::view_tiled_signal> on_tiled = [&] (wf::view_tiled_signal *ev)
        {
            if (ev && (ev->view.get() == toplevel) && (ev->new_edges == target_edges))
            {
                observed = true;
            }
        };
        view->connect(&on_tiled);
        wf::get_core().default_wm->tile_request(wayfire_toplevel_view{toplevel}, target_edges);
        on_tiled.disconnect();
        return observed || (toplevel->pending_tiled_edges() == target_edges);
    }

    bool set_view_minimized(wayfire_view view, wf::toplevel_view_interface_t *toplevel, bool minimized)
    {
        if (!view || !toplevel || !wf::get_core().default_wm)
        {
            return false;
        }
        if (toplevel->minimized == minimized)
        {
            return true;
        }
        bool observed = false;
        wf::signal::connection_t<wf::view_minimized_signal> on_minimized = [&] (wf::view_minimized_signal *ev)
        {
            if (ev && (ev->view.get() == toplevel) && (toplevel->minimized == minimized))
            {
                observed = true;
            }
        };
        view->connect(&on_minimized);
        wf::get_core().default_wm->minimize_request(wayfire_toplevel_view{toplevel}, minimized);
        on_minimized.disconnect();
        return observed || (toplevel->minimized == minimized);
    }

    void process_surface_state_request(const pending_surface_state_request_t& request,
        const std::unordered_map<std::string, wayfire_view>& views)
    {
        auto it = views.find(request.surface_id);
        if ((it == views.end()) || !it->second)
        {
            LOGW("agora-bridge: state target not found: ", request.surface_id);
            send_surface_state_response(request, false, "surface not found");
            return;
        }
        auto view = it->second;
        auto *toplevel = dynamic_cast<wf::toplevel_view_interface_t*>(view.get());
        if (!toplevel)
        {
            send_surface_state_response(request, false, "surface is not a toplevel");
            return;
        }
        const int requested_states = (request.fullscreen.has_value() ? 1 : 0) + (request.maximized.has_value() ? 1 : 0) + (request.minimized.has_value() ? 1 : 0);
        if (requested_states == 0)
        {
            send_surface_state_response(request, false, "no supported state requested");
            return;
        }
        if (requested_states > 1)
        {
            send_surface_state_response(request, false, "only one surface state may be requested");
            return;
        }
        if (request.fullscreen.has_value())
        {
            if (!set_view_fullscreen(view, toplevel, *request.fullscreen))
            {
                send_surface_state_response(request, false, "fullscreen state change was not observed");
                return;
            }
        } else if (request.maximized.has_value())
        {
            if (!set_view_maximized(view, toplevel, *request.maximized))
            {
                send_surface_state_response(request, false, "maximize state change was not observed");
                return;
            }
        } else if (request.minimized.has_value())
        {
            if (!set_view_minimized(view, toplevel, *request.minimized))
            {
                send_surface_state_response(request, false, "minimize state change was not observed");
                return;
            }
        }
        track_view(view);
        emit_surface_event(request.minimized.has_value() ? (*request.minimized ? "minimized" : "restored") : "focused", view);
        send_surface_state_response(request, true, "");
    }

    void send_property_response(const pending_property_request_t& request, bool ok, std::string_view error)
    {
        if (!bridge_)
        {
            return;
        }
        bridge_->send_line(agora::protocol::encode_property_response(request.request_id, request.surface_id, ok, error));
    }

    void send_focus_response(const pending_focus_request_t& request, bool ok, std::string_view error)
    {
        if (!bridge_)
        {
            return;
        }
        bridge_->send_line(agora::protocol::encode_focus_response(request.request_id, request.surface_id, ok, error));
    }

    void process_focus_request(const pending_focus_request_t& request,
        const std::unordered_map<std::string, wayfire_view>& views)
    {
        auto it = views.find(request.surface_id);
        if ((it == views.end()) || !it->second)
        {
            LOGW("agora-bridge: focus target not found: ", request.surface_id);
            send_focus_response(request, false, "surface not found");
            return;
        }
        auto view = it->second;
        if (!view->is_focusable() || !view->get_keyboard_focus_surface())
        {
            send_focus_response(request, false, "surface is not focusable");
            return;
        }
        wf::get_core().seat->focus_view(view);
        if (keyboard_focus_view_ != view)
        {
            send_focus_response(request, false, "focus not confirmed");
            return;
        }
        track_view(view);
        send_focus_response(request, true, "");
    }

    void process_property_request(const pending_property_request_t& request,
        const std::unordered_map<std::string, wayfire_view>& views)
    {
        auto it = views.find(request.surface_id);
        if ((it == views.end()) || !it->second)
        {
            LOGW("agora-bridge: property target not found: ", request.surface_id);
            send_property_response(request, false, "surface not found");
            return;
        }
        if (request.always_on_top.has_value())
        {
            if (!set_view_always_on_top(request.surface_id, it->second, *request.always_on_top))
            {
                send_property_response(request, false, "always_on_top state change was not observed");
                return;
            }
            track_view(it->second);
            emit_surface_event("focused", it->second);
            send_property_response(request, true, "");
        } else
        {
            send_property_response(request, false, "no supported property requested");
        }
    }

    void send_place_response(const pending_place_request_t& request, bool ok, std::string_view error)
    {
        if (!bridge_)
        {
            return;
        }
        bridge_->send_line(agora::protocol::encode_place_response(request.request_id, request.surface_id, ok, error));
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
        auto bbox = view->get_bounding_box();
        if ((bbox.width <= 0) || (bbox.height <= 0))
        {
            send_capture_error(request, "surface has empty dimensions");
            return;
        }

        wf::auxilliary_buffer_t snapshot_buffer;
        auto allocation = snapshot_buffer.allocate({bbox.width, bbox.height});
        if (allocation == wf::buffer_reallocation_result_t::FAILED)
        {
            send_capture_error(request, "snapshot buffer allocation failed");
            return;
        }
        view->take_snapshot(snapshot_buffer);

        wlr_buffer *capture_buffer = snapshot_buffer.get_buffer();
        if (!capture_buffer)
        {
            send_capture_error(request, "snapshot buffer not available");
            return;
        }

        uint32_t width = static_cast<uint32_t>(bbox.width);
        uint32_t height = static_cast<uint32_t>(bbox.height);
        if ((width == 0) || (height == 0))
        {
            send_capture_error(request, "surface buffer has empty dimensions");
            return;
        }

        std::vector<uint8_t> rgba(static_cast<size_t>(width) * height * 4);
        bool readback_ok = wf::gles::run_in_context_if_gles([&] ()
        {
            auto renderbuffer = snapshot_buffer.get_renderbuffer();
            wf::gles::bind_render_buffer(renderbuffer);
            // Wayfire's bind_render_buffer() binds GL_DRAW_FRAMEBUFFER for rendering.
            // glReadPixels() reads from GL_READ_FRAMEBUFFER, so bind the snapshot
            // buffer there explicitly before reading the rendered snapshot back.
            GL_CALL(glBindFramebuffer(GL_READ_FRAMEBUFFER, wf::gles::ensure_render_buffer_fb_id(renderbuffer)));
            GL_CALL(glPixelStorei(GL_PACK_ALIGNMENT, 1));
            GL_CALL(glReadPixels(0, 0, static_cast<GLsizei>(width), static_cast<GLsizei>(height),
                GL_RGBA, GL_UNSIGNED_BYTE, rgba.data()));
        });
        if (!readback_ok)
        {
            send_capture_error(request, "snapshot buffer does not support GL readback");
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

    agora::protocol::surface_snapshot_t snapshot_view_with_state(wayfire_view view)
    {
        auto snapshot = snapshot_view(view);
        auto state_it = always_on_top_by_surface_.find(snapshot.id);
        if (state_it != always_on_top_by_surface_.end())
        {
            snapshot.always_on_top = state_it->second;
        }
        if (auto *toplevel = dynamic_cast<wf::toplevel_view_interface_t*>(view.get()))
        {
            snapshot.fullscreen = toplevel->pending_fullscreen();
            const uint32_t tiled_edges = toplevel->pending_tiled_edges();
            snapshot.tiled_edges = tiled_edges;
            snapshot.maximized = (tiled_edges == wf::TILED_EDGES_ALL);
            snapshot.minimized = toplevel->minimized;
            snapshot.restorable = toplevel->minimized;
            snapshot.visibility_state = toplevel->minimized ? "minimized" : "visible";
        }
        annotate_stack_readback(snapshot, view);
        return snapshot;
    }

    void track_view(wayfire_view view)
    {
        if (!view)
        {
            return;
        }

        std::lock_guard lock(state_mutex_);
        auto snapshot = snapshot_view_with_state(view);
        views_by_surface_[snapshot.id] = view;
    }

    void forget_view(wayfire_view view)
    {
        if (!view)
        {
            return;
        }

        std::lock_guard lock(state_mutex_);
        auto snapshot = snapshot_view_with_state(view);
        views_by_surface_.erase(snapshot.id);
        always_on_top_by_surface_.erase(snapshot.id);
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
          case agora::protocol::bridge_message_kind::inject_input:
            queue_input_request(message.request_id, message.surface_id, message.coordinate_space,
                message.input_events);
            break;
          case agora::protocol::bridge_message_kind::place_surface:
            queue_place_surface(message.request_id, message.surface_id, wf::geometry_t{message.x, message.y, message.width, message.height});
            break;
          case agora::protocol::bridge_message_kind::focus_surface:
            queue_focus_surface(message.request_id, message.surface_id);
            break;
          case agora::protocol::bridge_message_kind::raise_surface:
            queue_raise_surface(message.request_id, message.surface_id, message.mode);
            break;
          case agora::protocol::bridge_message_kind::set_view_property:
            queue_property_request(message.request_id, message.surface_id, message.always_on_top);
            break;
          case agora::protocol::bridge_message_kind::set_surface_state:
            queue_surface_state_request(message.request_id, message.surface_id, message.fullscreen, message.maximized, message.minimized);
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

        agora::protocol::surface_snapshot_t snapshot;
        {
            std::lock_guard lock(state_mutex_);
            snapshot = snapshot_view_with_state(view);
        }
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
    std::unordered_map<std::string, bool> always_on_top_by_surface_;
    std::unordered_map<wlr_layer_surface_v1*, std::unique_ptr<layer_surface_record_t>> layer_surfaces_;
    wl_event_source *layer_scan_source_ = nullptr;
    std::vector<std::string> pending_close_surfaces_;
    std::vector<uint32_t> pending_close_owner_uids_;
    std::vector<pending_capture_request_t> pending_capture_requests_;
    std::vector<pending_input_request_t> pending_input_requests_;
    std::vector<pending_place_request_t> pending_place_requests_;
    std::vector<pending_property_request_t> pending_property_requests_;
    std::vector<pending_surface_state_request_t> pending_surface_state_requests_;
    std::vector<pending_focus_request_t> pending_focus_requests_;
    std::vector<pending_raise_request_t> pending_raise_requests_;
    bool pending_surface_resync_ = false;
    uint64_t z_order_generation_ = 1;
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

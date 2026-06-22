#pragma once

#include "policy_cache.hpp"

#include <algorithm>
#include <cctype>
#include <cstdint>
#include <cstdio>
#include <optional>
#include <regex>
#include <sstream>
#include <string>
#include <string_view>
#include <vector>

namespace agora::protocol
{
static constexpr const char *default_socket_path = "/run/agent-os/compositor-bridge.sock";

struct client_identity_t
{
    int32_t pid = -1;
    uint32_t uid = 0;
    uint32_t gid = 0;
};

struct surface_snapshot_t
{
    std::string id;
    uint32_t wayfire_view_id = 0;
    std::string app_id;
    std::string title;
    std::string role;
    std::string surface_kind = "xdg_view";
    std::string layer_namespace;
    std::string layer_name;
    std::vector<std::string> anchors;
    std::optional<int32_t> exclusive_zone;
    int32_t x = 0;
    int32_t y = 0;
    int32_t width = 0;
    int32_t height = 0;
    double scale_factor = 1.0;
    std::string output_id;
    std::optional<bool> always_on_top;
    std::optional<bool> fullscreen;
    int32_t workspace_x = 0;
    int32_t workspace_y = 0;
    std::string stack_layer;
    std::optional<int32_t> stack_index;
    std::optional<int32_t> stack_count;
    std::optional<bool> is_top_in_stack;
    uint64_t z_order_generation = 0;
};

enum class bridge_message_kind
{
    invalid,
    policy_replace,
    policy_upsert,
    policy_remove,
    input_context,
    close_surface,
    close_surfaces_by_uid,
    capture_surface,
    inject_input,
    place_surface,
    focus_surface,
    raise_surface,
    set_view_property,
    set_surface_state,
};

enum class input_event_kind
{
    invalid,
    pointer_move,
    pointer_button,
    key,
    scroll,
    touch,
};

struct input_event_t
{
    input_event_kind kind = input_event_kind::invalid;
    double x = 0;
    double y = 0;
    uint32_t button = 0;
    uint32_t keycode = 0;
    int32_t state = 0;
    int32_t discrete = 0;
    double value = 0;
    uint32_t axis = 0;
    int32_t touch_id = 0;
    std::string phase;
};

struct bridge_message_t
{
    bridge_message_kind kind = bridge_message_kind::invalid;
    std::vector<surface_policy_t> policies;
    std::string surface_id;
    std::string request_id;
    std::string coordinate_space;
    std::vector<input_event_t> input_events;
    int32_t x = 0;
    int32_t y = 0;
    int32_t width = 0;
    int32_t height = 0;
    std::optional<uint32_t> actor_uid;
    std::optional<uint32_t> owner_uid;
    std::optional<bool> always_on_top;
    std::optional<bool> fullscreen;
    std::string mode;
};

inline void append_unicode_escape(std::string& out, unsigned char ch)
{
    char buf[7];
    std::snprintf(buf, sizeof(buf), "\\u%04x", ch);
    out += buf;
}

inline std::string json_escape(std::string_view text)
{
    std::string out;
    out.reserve(text.size() + 8);
    for (unsigned char ch : text)
    {
        switch (ch)
        {
          case '\\': out += "\\\\"; break;
          case '"': out += "\\\""; break;
          case '\b': out += "\\b"; break;
          case '\f': out += "\\f"; break;
          case '\n': out += "\\n"; break;
          case '\r': out += "\\r"; break;
          case '\t': out += "\\t"; break;
          default:
            if (ch < 0x20)
            {
                append_unicode_escape(out, ch);
            } else
            {
                out += static_cast<char>(ch);
            }
            break;
        }
    }

    return out;
}

inline std::string json_unescape(std::string_view text)
{
    std::string out;
    out.reserve(text.size());
    for (size_t i = 0; i < text.size(); ++i)
    {
        char ch = text[i];
        if ((ch != '\\') || (i + 1 >= text.size()))
        {
            out += ch;
            continue;
        }

        char esc = text[++i];
        switch (esc)
        {
          case '\\': out += '\\'; break;
          case '"': out += '"'; break;
          case '/': out += '/'; break;
          case 'b': out += '\b'; break;
          case 'f': out += '\f'; break;
          case 'n': out += '\n'; break;
          case 'r': out += '\r'; break;
          case 't': out += '\t'; break;
          case 'u':
            if (i + 4 < text.size())
            {
                auto hex = std::string{text.substr(i + 1, 4)};
                unsigned value = std::stoul(hex, nullptr, 16);
                if (value <= 0x7F)
                {
                    out += static_cast<char>(value);
                } else
                {
                    out += '?';
                }
                i += 4;
            }
            break;
          default:
            out += esc;
            break;
        }
    }

    return out;
}

inline void append_layer_shell_metadata(std::ostringstream& out, const surface_snapshot_t& surface)
{
    if (surface.surface_kind != "layer_shell")
    {
        return;
    }
    out << ",\"layer_shell\":{";
    bool first = true;
    auto comma = [&first, &out]
    {
        if (!first)
        {
            out << ",";
        }
        first = false;
    };
    if (!surface.layer_namespace.empty())
    {
        comma();
        out << "\"namespace\":\"" << json_escape(surface.layer_namespace) << "\"";
    }
    if (!surface.layer_name.empty())
    {
        comma();
        out << "\"layer\":\"" << json_escape(surface.layer_name) << "\"";
    }
    if (!surface.anchors.empty())
    {
        comma();
        out << "\"anchors\":[";
        for (size_t i = 0; i < surface.anchors.size(); ++i)
        {
            if (i > 0)
            {
                out << ",";
            }
            out << "\"" << json_escape(surface.anchors[i]) << "\"";
        }
        out << "]";
    }
    if (surface.exclusive_zone.has_value())
    {
        comma();
        out << "\"exclusive_zone\":" << ((*surface.exclusive_zone) != 0 ? "true" : "false");
    }
    out << "}";
}

inline std::string encode_surface_event(std::string_view event_name, const surface_snapshot_t& surface,
    const client_identity_t& client, std::string_view device = "")
{
    std::ostringstream out;
    out << "{\"type\":\"surface_event\",\"event\":\"" << json_escape(event_name) << "\"";
    if (!device.empty())
    {
        out << ",\"device\":\"" << json_escape(device) << "\"";
    }

    out << ",\"surface\":{"
        << "\"id\":\"" << json_escape(surface.id) << "\","
        << "\"wayfire_view_id\":" << surface.wayfire_view_id << ","
        << "\"surface_kind\":\"" << json_escape(surface.surface_kind.empty() ? "xdg_view" : surface.surface_kind) << "\","
        << "\"app_id\":\"" << json_escape(surface.app_id) << "\","
        << "\"title\":\"" << json_escape(surface.title) << "\","
        << "\"role\":\"" << json_escape(surface.role) << "\","
        << "\"geometry\":{" << "\"x\":" << surface.x << "," << "\"y\":" << surface.y << ","
        << "\"width\":" << surface.width << "," << "\"height\":" << surface.height << "},"
        << "\"pixel_size\":{" << "\"x\":0,\"y\":0,"
        << "\"width\":" << static_cast<int32_t>(surface.width * surface.scale_factor) << ","
        << "\"height\":" << static_cast<int32_t>(surface.height * surface.scale_factor) << "},"
        << "\"scale_factor\":" << surface.scale_factor << ","
        << "\"visible\":true,"
        << "\"output_id\":\"" << json_escape(surface.output_id) << "\"";
    if (!surface.stack_layer.empty())
    {
        out << ",\"workspace\":{\"x\":" << surface.workspace_x << ",\"y\":" << surface.workspace_y << "}";
        out << ",\"stack_layer\":\"" << json_escape(surface.stack_layer) << "\"";
    }
    if (surface.stack_index.has_value())
    {
        out << ",\"stack_index\":" << *surface.stack_index;
    }
    if (surface.stack_count.has_value())
    {
        out << ",\"stack_count\":" << *surface.stack_count;
    }
    if (surface.is_top_in_stack.has_value())
    {
        out << ",\"is_top_in_stack\":" << (*surface.is_top_in_stack ? "true" : "false");
    }
    if (surface.z_order_generation > 0)
    {
        out << ",\"z_order_generation\":" << surface.z_order_generation;
    }
    if (surface.always_on_top.has_value())
    {
        out << ",\"always_on_top\":" << (*surface.always_on_top ? "true" : "false");
    }
    if (surface.fullscreen.has_value())
    {
        out << ",\"fullscreen\":" << (*surface.fullscreen ? "true" : "false");
    }
    append_layer_shell_metadata(out, surface);
    out << "},"
        << "\"client\":{" << "\"pid\":" << client.pid << ","
        << "\"uid\":" << client.uid << ","
        << "\"gid\":" << client.gid << "}}";
    return out.str();
}

inline std::string encode_capture_response(std::string_view request_id, std::string_view surface_id,
    bool ok, uint32_t width, uint32_t height, std::string_view format, std::string_view data_base64,
    std::string_view error = "")
{
    std::ostringstream out;
    out << "{\"type\":\"capture_response\","
        << "\"request_id\":\"" << json_escape(request_id) << "\","
        << "\"surface_id\":\"" << json_escape(surface_id) << "\","
        << "\"ok\":" << (ok ? "true" : "false");
    if (ok)
    {
        out << ",\"width\":" << width
            << ",\"height\":" << height
            << ",\"format\":\"" << json_escape(format) << "\""
            << ",\"data_base64\":\"" << json_escape(data_base64) << "\"";
    } else
    {
        out << ",\"error\":\"" << json_escape(error) << "\"";
    }

    out << "}";
    return out.str();
}


inline std::string encode_place_response(std::string_view request_id, std::string_view surface_id,
    bool ok, std::string_view error = "")
{
    std::ostringstream out;
    out << "{\"type\":\"place_response\","
        << "\"request_id\":\"" << json_escape(request_id) << "\","
        << "\"surface_id\":\"" << json_escape(surface_id) << "\","
        << "\"ok\":" << (ok ? "true" : "false");
    if (!ok || !error.empty())
    {
        out << ",\"error\":\"" << json_escape(error) << "\"";
    }
    out << "}";
    return out.str();
}

inline std::string encode_focus_response(std::string_view request_id, std::string_view surface_id,
    bool ok, std::string_view error = "")
{
    std::ostringstream out;
    out << "{\"type\":\"focus_response\","
        << "\"request_id\":\"" << json_escape(request_id) << "\","
        << "\"surface_id\":\"" << json_escape(surface_id) << "\","
        << "\"ok\":" << (ok ? "true" : "false");
    if (!ok || !error.empty())
    {
        out << ",\"error\":\"" << json_escape(error) << "\"";
    }
    out << "}";
    return out.str();
}

inline std::string encode_raise_response(std::string_view request_id, std::string_view surface_id,
    bool ok, std::string_view error = "")
{
    std::ostringstream out;
    out << "{\"type\":\"raise_response\","
        << "\"request_id\":\"" << json_escape(request_id) << "\","
        << "\"surface_id\":\"" << json_escape(surface_id) << "\","
        << "\"ok\":" << (ok ? "true" : "false");
    if (!ok || !error.empty())
    {
        out << ",\"error\":\"" << json_escape(error) << "\"";
    }
    out << "}";
    return out.str();
}

inline std::string encode_property_response(std::string_view request_id, std::string_view surface_id,
    bool ok, std::string_view error = "")
{
    std::ostringstream out;
    out << "{\"type\":\"property_response\","
        << "\"request_id\":\"" << json_escape(request_id) << "\","
        << "\"surface_id\":\"" << json_escape(surface_id) << "\","
        << "\"ok\":" << (ok ? "true" : "false");
    if (!ok || !error.empty())
    {
        out << ",\"error\":\"" << json_escape(error) << "\"";
    }
    out << "}";
    return out.str();
}

inline std::string encode_surface_state_response(std::string_view request_id, std::string_view surface_id,
    bool ok, std::string_view error = "")
{
    std::ostringstream out;
    out << "{\"type\":\"surface_state_response\","
        << "\"request_id\":\"" << json_escape(request_id) << "\","
        << "\"surface_id\":\"" << json_escape(surface_id) << "\","
        << "\"ok\":" << (ok ? "true" : "false");
    if (!ok || !error.empty())
    {
        out << ",\"error\":\"" << json_escape(error) << "\"";
    }
    out << "}";
    return out.str();
}

inline std::string encode_input_response(std::string_view request_id, std::string_view surface_id,
    bool ok, uint32_t accepted, uint32_t rejected, std::string_view error = "")
{
    std::ostringstream out;
    out << "{\"type\":\"input_response\","
        << "\"request_id\":\"" << json_escape(request_id) << "\","
        << "\"surface_id\":\"" << json_escape(surface_id) << "\","
        << "\"ok\":" << (ok ? "true" : "false")
        << ",\"accepted\":" << accepted
        << ",\"rejected\":" << rejected;
    if (!ok || !error.empty())
    {
        out << ",\"error\":\"" << json_escape(error) << "\"";
    }

    out << "}";
    return out.str();
}

inline std::optional<std::string> find_string_field(const std::string& text, const char *key)
{
    std::regex re(std::string{"\""} + key + "\"\\s*:\\s*\"((?:[^\"\\\\]|\\\\.)*)\"");
    std::smatch match;
    if (std::regex_search(text, match, re) && (match.size() > 1))
    {
        return json_unescape(match[1].str());
    }

    return std::nullopt;
}

inline std::optional<uint32_t> find_uint_field(const std::string& text, const char *key)
{
    std::regex re(std::string{"\""} + key + "\"\\s*:\\s*([0-9]+)");
    std::smatch match;
    if (std::regex_search(text, match, re) && (match.size() > 1))
    {
        return static_cast<uint32_t>(std::stoul(match[1].str()));
    }

    return std::nullopt;
}

inline std::optional<int32_t> find_int_field(const std::string& text, const char *key)
{
    std::regex re(std::string{"\""} + key + "\"\\s*:\\s*(-?[0-9]+)");
    std::smatch match;
    if (std::regex_search(text, match, re) && (match.size() > 1))
    {
        return static_cast<int32_t>(std::stol(match[1].str()));
    }

    return std::nullopt;
}

inline std::optional<bool> find_bool_field(const std::string& text, const char *key)
{
    std::regex re(std::string{"\""} + key + "\"\\s*:\\s*(true|false)");
    std::smatch match;
    if (std::regex_search(text, match, re) && (match.size() > 1))
    {
        return match[1].str() == "true";
    }

    return std::nullopt;
}

inline std::optional<double> find_number_field(const std::string& text, const char *key)
{
    std::regex re(std::string{"\""} + key + "\"\\s*:\\s*(-?[0-9]+(?:\\.[0-9]+)?)");
    std::smatch match;
    if (std::regex_search(text, match, re) && (match.size() > 1))
    {
        return std::stod(match[1].str());
    }

    return std::nullopt;
}

inline std::optional<std::string> find_braced_region(const std::string& text, const char *key,
    char open_ch, char close_ch)
{
    auto key_pos = text.find(std::string{"\""} + key + "\"");
    if (key_pos == std::string::npos)
    {
        return std::nullopt;
    }

    auto start = text.find(open_ch, key_pos);
    if (start == std::string::npos)
    {
        return std::nullopt;
    }

    int depth = 0;
    bool in_string = false;
    bool escaped = false;
    for (size_t i = start; i < text.size(); ++i)
    {
        char ch = text[i];
        if (in_string)
        {
            if (escaped)
            {
                escaped = false;
            } else if (ch == '\\')
            {
                escaped = true;
            } else if (ch == '"')
            {
                in_string = false;
            }
            continue;
        }

        if (ch == '"')
        {
            in_string = true;
            continue;
        }

        if (ch == open_ch)
        {
            ++depth;
        } else if (ch == close_ch)
        {
            --depth;
            if (depth == 0)
            {
                return text.substr(start, i - start + 1);
            }
        }
    }

    return std::nullopt;
}

inline std::vector<uint32_t> parse_uint_array(std::string text)
{
    std::vector<uint32_t> values;
    text.erase(std::remove_if(text.begin(), text.end(),
        [] (unsigned char ch)
    {
        return std::isspace(ch) != 0;
    }), text.end());

    if ((text.size() < 2) || (text.front() != '[') || (text.back() != ']'))
    {
        return values;
    }

    text = text.substr(1, text.size() - 2);
    std::stringstream stream(text);
    std::string part;
    while (std::getline(stream, part, ','))
    {
        if (!part.empty())
        {
            values.push_back(static_cast<uint32_t>(std::stoul(part)));
        }
    }

    return values;
}

inline std::vector<uint32_t> find_uint_array_field(const std::string& text, const char *key)
{
    auto array = find_braced_region(text, key, '[', ']');
    if (!array.has_value())
    {
        return {};
    }

    return parse_uint_array(*array);
}

inline std::vector<std::string> split_top_level_objects(const std::string& array_text)
{
    std::vector<std::string> objects;
    int depth = 0;
    bool in_string = false;
    bool escaped = false;
    size_t current_start = std::string::npos;
    for (size_t i = 0; i < array_text.size(); ++i)
    {
        char ch = array_text[i];
        if (in_string)
        {
            if (escaped)
            {
                escaped = false;
            } else if (ch == '\\')
            {
                escaped = true;
            } else if (ch == '"')
            {
                in_string = false;
            }
            continue;
        }

        if (ch == '"')
        {
            in_string = true;
            continue;
        }

        if (ch == '{')
        {
            if (depth == 0)
            {
                current_start = i;
            }

            ++depth;
        } else if (ch == '}')
        {
            --depth;
            if ((depth == 0) && (current_start != std::string::npos))
            {
                objects.push_back(array_text.substr(current_start, i - current_start + 1));
                current_start = std::string::npos;
            }
        }
    }

    return objects;
}

inline surface_policy_t parse_surface_policy(const std::string& text)
{
    surface_policy_t policy;
    policy.surface_id = find_string_field(text, "surface_id").value_or("");
    policy.owner_uid = find_uint_field(text, "owner_uid").value_or(0);
    for (auto uid : find_uint_array_field(text, "allow_pointer_uids"))
    {
        policy.allow_pointer_uids.insert(uid);
    }

    for (auto uid : find_uint_array_field(text, "allow_keyboard_uids"))
    {
        policy.allow_keyboard_uids.insert(uid);
    }

    return policy;
}

inline int32_t parse_input_state(const std::string& text)
{
    auto state = find_string_field(text, "state").value_or("");
    if ((state == "pressed") || (state == "down"))
    {
        return 1;
    }
    if ((state == "released") || (state == "up"))
    {
        return 0;
    }

    return find_int_field(text, "state").value_or(0);
}

inline input_event_t parse_input_event(const std::string& text)
{
    input_event_t event;
    auto type = find_string_field(text, "type").value_or("");
    if (type == "pointer_move")
    {
        event.kind = input_event_kind::pointer_move;
    } else if (type == "pointer_button")
    {
        event.kind = input_event_kind::pointer_button;
    } else if (type == "key")
    {
        event.kind = input_event_kind::key;
    } else if (type == "scroll")
    {
        event.kind = input_event_kind::scroll;
    } else if (type == "touch")
    {
        event.kind = input_event_kind::touch;
    }

    event.x = find_number_field(text, "x").value_or(0);
    event.y = find_number_field(text, "y").value_or(0);
    event.button = find_uint_field(text, "button").value_or(0);
    event.keycode = find_uint_field(text, "keycode").value_or(0);
    event.state = parse_input_state(text);
    event.value = find_number_field(text, "value").value_or(0);
    event.discrete = find_int_field(text, "discrete").value_or(0);
    event.axis = find_uint_field(text, "axis").value_or(0);
    event.touch_id = find_int_field(text, "touch_id").value_or(0);
    event.phase = find_string_field(text, "phase").value_or("");
    return event;
}

inline bridge_message_t parse_bridge_message(const std::string& line)
{
    bridge_message_t message;
    auto type = find_string_field(line, "type");
    if (!type.has_value())
    {
        return message;
    }

    if (*type == "policy_replace")
    {
        message.kind = bridge_message_kind::policy_replace;
        auto array = find_braced_region(line, "surfaces", '[', ']');
        if (array.has_value())
        {
            for (const auto& object : split_top_level_objects(*array))
            {
                message.policies.push_back(parse_surface_policy(object));
            }
        }

        return message;
    }

    if (*type == "policy_upsert")
    {
        message.kind = bridge_message_kind::policy_upsert;
        auto object = find_braced_region(line, "surface", '{', '}');
        if (object.has_value())
        {
            message.policies.push_back(parse_surface_policy(*object));
        }

        return message;
    }

    if (*type == "policy_remove")
    {
        message.kind = bridge_message_kind::policy_remove;
        message.surface_id = find_string_field(line, "surface_id").value_or("");
        return message;
    }

    if (*type == "input_context")
    {
        message.kind = bridge_message_kind::input_context;
        message.actor_uid = find_uint_field(line, "actor_uid");
        return message;
    }

    if (*type == "close_surface")
    {
        message.kind = bridge_message_kind::close_surface;
        message.surface_id = find_string_field(line, "surface_id").value_or("");
        return message;
    }

    if (*type == "close_surfaces_by_uid")
    {
        message.kind = bridge_message_kind::close_surfaces_by_uid;
        message.owner_uid = find_uint_field(line, "owner_uid");
        return message;
    }

    if (*type == "capture_surface")
    {
        message.kind = bridge_message_kind::capture_surface;
        message.request_id = find_string_field(line, "request_id").value_or("");
        message.surface_id = find_string_field(line, "surface_id").value_or("");
        return message;
    }


    if (*type == "place_surface")
    {
        message.kind = bridge_message_kind::place_surface;
        message.request_id = find_string_field(line, "request_id").value_or("");
        message.surface_id = find_string_field(line, "surface_id").value_or("");
        auto geom = find_braced_region(line, "geometry", '{', '}');
        if (geom.has_value())
        {
            message.x = find_int_field(*geom, "x").value_or(0);
            message.y = find_int_field(*geom, "y").value_or(0);
            message.width = find_int_field(*geom, "width").value_or(0);
            message.height = find_int_field(*geom, "height").value_or(0);
        }
        return message;
    }

    if (*type == "focus_surface")
    {
        message.kind = bridge_message_kind::focus_surface;
        message.request_id = find_string_field(line, "request_id").value_or("");
        message.surface_id = find_string_field(line, "surface_id").value_or("");
        return message;
    }

    if (*type == "raise_surface")
    {
        message.kind = bridge_message_kind::raise_surface;
        message.request_id = find_string_field(line, "request_id").value_or("");
        message.surface_id = find_string_field(line, "surface_id").value_or("");
        message.mode = find_string_field(line, "mode").value_or("no-focus");
        return message;
    }

    if (*type == "set_view_property")
    {
        message.kind = bridge_message_kind::set_view_property;
        message.request_id = find_string_field(line, "request_id").value_or("");
        message.surface_id = find_string_field(line, "surface_id").value_or("");
        auto properties = find_braced_region(line, "properties", '{', '}');
        if (properties.has_value())
        {
            message.always_on_top = find_bool_field(*properties, "always_on_top");
        } else
        {
            message.always_on_top = find_bool_field(line, "always_on_top");
        }
        return message;
    }

    if (*type == "set_surface_state")
    {
        message.kind = bridge_message_kind::set_surface_state;
        message.request_id = find_string_field(line, "request_id").value_or("");
        message.surface_id = find_string_field(line, "surface_id").value_or("");
        message.fullscreen = find_bool_field(line, "fullscreen");
        return message;
    }

    if (*type == "inject_input")
    {
        message.kind = bridge_message_kind::inject_input;
        message.request_id = find_string_field(line, "request_id").value_or("");
        message.surface_id = find_string_field(line, "surface_id").value_or("");
        message.coordinate_space = find_string_field(line, "coordinate_space").value_or("surface");
        auto array = find_braced_region(line, "events", '[', ']');
        if (array.has_value())
        {
            for (const auto& object : split_top_level_objects(*array))
            {
                message.input_events.push_back(parse_input_event(object));
            }
        }

        return message;
    }

    return message;
}
}

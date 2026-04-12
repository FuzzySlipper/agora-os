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
};

enum class bridge_message_kind
{
    invalid,
    policy_replace,
    policy_upsert,
    policy_remove,
    input_context,
};

struct bridge_message_t
{
    bridge_message_kind kind = bridge_message_kind::invalid;
    std::vector<surface_policy_t> policies;
    std::string surface_id;
    std::optional<uint32_t> actor_uid;
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
        << "\"app_id\":\"" << json_escape(surface.app_id) << "\","
        << "\"title\":\"" << json_escape(surface.title) << "\","
        << "\"role\":\"" << json_escape(surface.role) << "\"},"
        << "\"client\":{"
        << "\"pid\":" << client.pid << ","
        << "\"uid\":" << client.uid << ","
        << "\"gid\":" << client.gid << "}}";
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

    return message;
}
}

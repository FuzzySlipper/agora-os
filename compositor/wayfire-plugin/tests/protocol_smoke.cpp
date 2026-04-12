#include "../src/policy_cache.hpp"
#include "../src/protocol.hpp"

#include <cassert>
#include <optional>
#include <string>

int main()
{
    using namespace agora;
    using namespace agora::protocol;

    std::string noisy = "ab";
    noisy.insert(noisy.begin() + 1, static_cast<char>(1));
    noisy.push_back('\n');
    noisy.push_back('\t');
    noisy.push_back('"');
    noisy.push_back('\\');

    auto escaped = json_escape(noisy);
    assert(escaped.find("\\u0001") != std::string::npos);
    assert(escaped.find("\\n") != std::string::npos);
    assert(escaped.find("\\t") != std::string::npos);
    assert(escaped.find("\\\"") != std::string::npos);
    assert(escaped.find("\\\\") != std::string::npos);

    auto remove_msg = parse_bridge_message("{\"type\":\"policy_remove\",\"surface_id\":\"view-7\"}");
    assert(remove_msg.kind == bridge_message_kind::policy_remove);
    assert(remove_msg.surface_id == "view-7");

    auto escaped_string_msg = parse_bridge_message(
        "{\"type\":\"policy_remove\",\"surface_id\":\"view-\\\"7\"}");
    assert(escaped_string_msg.surface_id == "view-\"7");

    auto agent_msg = parse_bridge_message("{\"type\":\"input_context\",\"actor_uid\":60002}");
    assert(agent_msg.kind == bridge_message_kind::input_context);
    assert(agent_msg.actor_uid.has_value() && (*agent_msg.actor_uid == 60002));

    auto human_msg = parse_bridge_message("{\"type\":\"input_context\"}");
    assert(human_msg.kind == bridge_message_kind::input_context);
    assert(human_msg.actor_uid == std::nullopt);

    auto close_surface_msg = parse_bridge_message(
        "{\"type\":\"close_surface\",\"surface_id\":\"view-9\"}");
    assert(close_surface_msg.kind == bridge_message_kind::close_surface);
    assert(close_surface_msg.surface_id == "view-9");

    auto close_uid_msg = parse_bridge_message(
        "{\"type\":\"close_surfaces_by_uid\",\"owner_uid\":60003}");
    assert(close_uid_msg.kind == bridge_message_kind::close_surfaces_by_uid);
    assert(close_uid_msg.owner_uid.has_value() && (*close_uid_msg.owner_uid == 60003));

    policy_cache_t cache;
    cache.set_actor_uid(std::nullopt);
    assert(cache.allows("view-1", input_device_t::keyboard));

    surface_policy_t policy;
    policy.surface_id = "view-1";
    policy.owner_uid = 60001;
    policy.allow_keyboard_uids.insert(0);
    cache.upsert(policy);

    cache.set_actor_uid(0);
    assert(cache.allows("view-1", input_device_t::keyboard));

    cache.set_actor_uid(60002);
    assert(cache.allows("view-1", input_device_t::keyboard) == false);

    return 0;
}

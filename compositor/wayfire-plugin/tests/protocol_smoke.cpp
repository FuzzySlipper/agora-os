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

    auto property_msg = parse_bridge_message(
        "{\"type\":\"set_view_property\",\"surface_id\":\"view-10\","
        "\"properties\":{\"always_on_top\":true}}");
    assert(property_msg.kind == bridge_message_kind::set_view_property);
    assert(property_msg.surface_id == "view-10");
    assert(property_msg.always_on_top.has_value() && (*property_msg.always_on_top == true));

    auto focus_msg = parse_bridge_message(
        "{\"type\":\"focus_surface\",\"request_id\":\"focus-1\",\"surface_id\":\"view-10\"}");
    assert(focus_msg.kind == bridge_message_kind::focus_surface);
    assert(focus_msg.request_id == "focus-1");
    assert(focus_msg.surface_id == "view-10");

    auto raise_msg = parse_bridge_message(
        "{\"type\":\"raise_surface\",\"request_id\":\"raise-1\",\"surface_id\":\"view-10\",\"mode\":\"no-focus\"}");
    assert(raise_msg.kind == bridge_message_kind::raise_surface);
    assert(raise_msg.request_id == "raise-1");
    assert(raise_msg.surface_id == "view-10");
    assert(raise_msg.mode == "no-focus");

    auto raise_response = encode_raise_response("raise-1", "view-10", true, "");
    assert(raise_response.find("\"type\":\"raise_response\"") != std::string::npos);
    assert(raise_response.find("\"ok\":true") != std::string::npos);

    auto focus_response = encode_focus_response("focus-1", "view-10", true, "");
    assert(focus_response.find("\"type\":\"focus_response\"") != std::string::npos);
    assert(focus_response.find("\"ok\":true") != std::string::npos);

    auto focus_error = encode_focus_response("focus-2", "view-missing", false, "surface not found");
    assert(focus_error.find("surface not found") != std::string::npos);

    auto inject_msg = parse_bridge_message(
        "{\"type\":\"inject_input\",\"request_id\":\"input-1\",\"surface_id\":\"view-11\","
        "\"coordinate_space\":\"surface-local\",\"events\":["
        "{\"type\":\"pointer_move\",\"x\":12.5,\"y\":34.25},"
        "{\"type\":\"pointer_button\",\"button\":272,\"state\":\"pressed\"},"
        "{\"type\":\"key\",\"keycode\":28,\"state\":\"released\"},"
        "{\"type\":\"scroll\",\"axis\":1,\"value\":-15.0,\"discrete\":-1},"
        "{\"type\":\"touch\",\"touch_id\":7,\"phase\":\"down\",\"x\":1,\"y\":2}"
        "]}");
    assert(inject_msg.kind == bridge_message_kind::inject_input);
    assert(inject_msg.request_id == "input-1");
    assert(inject_msg.surface_id == "view-11");
    assert(inject_msg.coordinate_space == "surface-local");
    assert(inject_msg.input_events.size() == 5);
    assert(inject_msg.input_events[0].kind == input_event_kind::pointer_move);
    assert(inject_msg.input_events[0].x == 12.5);
    assert(inject_msg.input_events[0].y == 34.25);
    assert(inject_msg.input_events[1].kind == input_event_kind::pointer_button);
    assert(inject_msg.input_events[1].button == 272);
    assert(inject_msg.input_events[1].state == 1);
    assert(inject_msg.input_events[2].kind == input_event_kind::key);
    assert(inject_msg.input_events[2].keycode == 28);
    assert(inject_msg.input_events[2].state == 0);
    assert(inject_msg.input_events[3].kind == input_event_kind::scroll);
    assert(inject_msg.input_events[3].axis == 1);
    assert(inject_msg.input_events[3].discrete == -1);
    assert(inject_msg.input_events[4].kind == input_event_kind::touch);
    assert(inject_msg.input_events[4].touch_id == 7);
    assert(inject_msg.input_events[4].phase == "down");

    auto input_response = encode_input_response("input-1", "view-11", true, 5, 0, "");
    assert(input_response.find("\"type\":\"input_response\"") != std::string::npos);
    assert(input_response.find("\"accepted\":5") != std::string::npos);
    assert(input_response.find("\"rejected\":0") != std::string::npos);

    auto input_error = encode_input_response("input-2", "view-12", false, 0, 1, "unsupported coordinate_space");
    assert(input_error.find("unsupported coordinate_space") != std::string::npos);

    surface_snapshot_t layer_surface;
    layer_surface.id = "layer-shell-4242";
    layer_surface.surface_kind = "layer_shell";
    layer_surface.app_id = "io.agoraos.WebviewLauncher";
    layer_surface.title = "Agora Desktop Shell";
    layer_surface.role = "panel";
    layer_surface.layer_namespace = "agora-webview";
    layer_surface.layer_name = "top";
    layer_surface.anchors = {"top"};
    layer_surface.exclusive_zone = 48;
    layer_surface.output_id = "HDMI-A-1";
    layer_surface.workspace_x = 1;
    layer_surface.workspace_y = 2;
    layer_surface.stack_layer = "top";
    layer_surface.stack_index = 3;
    layer_surface.stack_count = 4;
    layer_surface.is_top_in_stack = true;
    layer_surface.z_order_generation = 9;

    client_identity_t layer_client;
    layer_client.pid = 4242;
    layer_client.uid = 60001;
    layer_client.gid = 60001;

    auto layer_mapped = encode_surface_event("mapped", layer_surface, layer_client);
    assert(layer_mapped.find("\"event\":\"mapped\"") != std::string::npos);
    assert(layer_mapped.find("\"id\":\"layer-shell-4242\"") != std::string::npos);
    assert(layer_mapped.find("\"surface_kind\":\"layer_shell\"") != std::string::npos);
    assert(layer_mapped.find("\"wayfire_view_id\":0") != std::string::npos);
    assert(layer_mapped.find("\"role\":\"panel\"") != std::string::npos);
    assert(layer_mapped.find("\"layer_shell\":{") != std::string::npos);
    assert(layer_mapped.find("\"namespace\":\"agora-webview\"") != std::string::npos);
    assert(layer_mapped.find("\"anchors\":[\"top\"]") != std::string::npos);
    assert(layer_mapped.find("\"exclusive_zone\":true") != std::string::npos);
    assert(layer_mapped.find("\"workspace\":{\"x\":1,\"y\":2}") != std::string::npos);
    assert(layer_mapped.find("\"stack_layer\":\"top\"") != std::string::npos);
    assert(layer_mapped.find("\"stack_index\":3") != std::string::npos);
    assert(layer_mapped.find("\"stack_count\":4") != std::string::npos);
    assert(layer_mapped.find("\"is_top_in_stack\":true") != std::string::npos);
    assert(layer_mapped.find("\"z_order_generation\":9") != std::string::npos);
    assert(layer_mapped.find("\"pid\":4242") != std::string::npos);

    auto layer_unmapped = encode_surface_event("unmapped", layer_surface, layer_client);
    assert(layer_unmapped.find("\"event\":\"unmapped\"") != std::string::npos);
    assert(layer_unmapped.find("\"surface_kind\":\"layer_shell\"") != std::string::npos);

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

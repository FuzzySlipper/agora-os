#pragma once

#include <cstdint>
#include <mutex>
#include <shared_mutex>
#include <string>
#include <string_view>
#include <unordered_map>
#include <unordered_set>
#include <vector>

namespace agora
{
enum class input_device_t
{
    pointer,
    keyboard,
};

struct surface_policy_t
{
    std::string surface_id;
    uint32_t owner_uid = 0;
    std::unordered_set<uint32_t> allow_pointer_uids;
    std::unordered_set<uint32_t> allow_keyboard_uids;
};

class policy_cache_t
{
  public:
    void replace(std::vector<surface_policy_t> policies)
    {
        std::unique_lock lock(this->mutex_);
        this->by_surface_.clear();
        for (auto& policy : policies)
        {
            this->by_surface_[policy.surface_id] = std::move(policy);
        }
    }

    void upsert(surface_policy_t policy)
    {
        std::unique_lock lock(this->mutex_);
        this->by_surface_[policy.surface_id] = std::move(policy);
    }

    void erase(std::string_view surface_id)
    {
        std::unique_lock lock(this->mutex_);
        this->by_surface_.erase(std::string(surface_id));
    }

    void set_actor_uid(uint32_t actor_uid)
    {
        std::unique_lock lock(this->mutex_);
        this->actor_uid_ = actor_uid;
    }

    uint32_t actor_uid() const
    {
        std::shared_lock lock(this->mutex_);
        return this->actor_uid_;
    }

    bool allows(std::string_view surface_id, input_device_t device) const
    {
        std::shared_lock lock(this->mutex_);
        if (this->actor_uid_ == 0)
        {
            return true;
        }

        auto it = this->by_surface_.find(std::string(surface_id));
        if (it == this->by_surface_.end())
        {
            return false;
        }

        const auto& policy = it->second;
        if (policy.owner_uid == this->actor_uid_)
        {
            return true;
        }

        const auto& allowed = (device == input_device_t::pointer) ?
            policy.allow_pointer_uids :
            policy.allow_keyboard_uids;
        return allowed.count(this->actor_uid_) > 0;
    }

  private:
    mutable std::shared_mutex mutex_;
    std::unordered_map<std::string, surface_policy_t> by_surface_;
    uint32_t actor_uid_ = 0;
};
}

package channels

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"reflect"

	"github.com/xibodev/compa/v3/pkg/config"
)

func toChannelHashes(cfg *config.Config) map[string]string {
	result := make(map[string]string)
	ch := cfg.Channels
	marshal, err := json.Marshal(ch)
	if err != nil {
		log.Printf("[manager_channel] failed to marshal channels config: %v", err)
		return result
	}
	var channelConfig map[string]map[string]any
	if err := json.Unmarshal(marshal, &channelConfig); err != nil {
		log.Printf("[manager_channel] failed to unmarshal channels config: %v", err)
		return result
	}

	for key, value := range channelConfig {
		if enabled, ok := value["enabled"].(bool); !ok || !enabled {
			continue
		}
		hiddenValues(value, ch.Get(key))
		valueBytes, err := json.Marshal(value)
		if err != nil {
			log.Printf("[manager_channel] failed to marshal channel %s config: %v", key, err)
			continue
		}
		hash := md5.Sum(valueBytes)
		result[key] = hex.EncodeToString(hash[:])
	}

	return result
}

// hiddenValues adds to value the channel's secrets, which its JSON leaves
// out, so that a change to a secret alone also changes the channel's hash
// and a reload restarts the channel with it.
func hiddenValues(value map[string]any, ch *config.Channel) {
	decoded, err := ch.GetDecoded()
	if err != nil || decoded == nil {
		return
	}
	secrets := map[string]string{}
	collectSecrets(reflect.ValueOf(decoded), "", secrets)
	if len(secrets) > 0 {
		value["secrets"] = secrets
	}
}

// collectSecrets adds the value of every secret setting in v to out, keyed
// by its path in v.
func collectSecrets(v reflect.Value, path string, out map[string]string) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			collectSecrets(v.Elem(), path, out)
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[config.SecureString]() {
			s := v.Interface().(config.SecureString)
			out[path] = s.String()
			return
		}
		for i := range v.NumField() {
			if field := v.Type().Field(i); field.IsExported() {
				collectSecrets(v.Field(i), path+"."+field.Name, out)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			collectSecrets(v.Index(i), fmt.Sprintf("%s[%d]", path, i), out)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			collectSecrets(iter.Value(), fmt.Sprintf("%s[%v]", path, iter.Key()), out)
		}
	}
}

func compareChannels(old, news map[string]string) (added, removed []string) {
	for key, newHash := range news {
		if oldHash, ok := old[key]; ok {
			if newHash != oldHash {
				removed = append(removed, key)
				added = append(added, key)
			}
		} else {
			added = append(added, key)
		}
	}
	for key := range old {
		if _, ok := news[key]; !ok {
			removed = append(removed, key)
		}
	}
	return added, removed
}

func toChannelConfig(cfg *config.Config, list []string) (*config.ChannelsConfig, error) {
	result := make(config.ChannelsConfig)
	for _, name := range list {
		bc, ok := cfg.Channels[name]
		if !ok || !bc.Enabled {
			continue
		}
		result[name] = bc
	}
	return &result, nil
}

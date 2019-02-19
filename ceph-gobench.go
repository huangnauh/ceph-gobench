package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"
)

//future feature
func makepreudorandom() {
	a := make([]int, 0, 4096/4)
	for i := 0; i < 4096; i += 4 {
		a = append(a, i)
	}
	rand.Shuffle(len(a), func(i, j int) {
		a[i], a[j] = a[j], a[i]
	})
	fmt.Println(a)
}

func MakeMonQuery(cephconn *Cephconnection, query map[string]string) []byte {
	monjson, err := json.Marshal(query)
	if err != nil {
		log.Fatalf("Can't marshal json mon query. Error: %v", err)
	}

	monrawanswer, _, err := cephconn.conn.MonCommand(monjson)
	if err != nil {
		log.Fatalf("Failed exec monCommand. Error: %v", err)
	}
	return monrawanswer
}

func GetPoolSize(cephconn *Cephconnection, params Params) Poolinfo {
	monrawanswer := MakeMonQuery(cephconn, map[string]string{"prefix": "osd pool get", "pool": params.pool,
		"format": "json", "var": "size"})
	monanswer := Poolinfo{}
	if err := json.Unmarshal([]byte(monrawanswer), &monanswer); err != nil {
		log.Fatalf("Can't parse monitor answer. Error: %v", err)
	}
	return monanswer

}

func GetPgByPool(cephconn *Cephconnection, params Params) []PlacementGroup {
	monrawanswer := MakeMonQuery(cephconn, map[string]string{"prefix": "pg ls-by-pool", "poolstr": params.pool,
		"format": "json"})
	var monanswer []PlacementGroup
	if err := json.Unmarshal([]byte(monrawanswer), &monanswer); err != nil {
		log.Fatalf("Can't parse monitor answer. Error: %v", err)
	}
	return monanswer
}

func GetOsdCrushDump(cephconn *Cephconnection) OsdCrushDump {
	monrawanswer := MakeMonQuery(cephconn, map[string]string{"prefix": "osd crush dump", "format": "json"})
	var monanswer OsdCrushDump
	if err := json.Unmarshal([]byte(monrawanswer), &monanswer); err != nil {
		log.Fatalf("Can't parse monitor answer. Error: %v", err)
	}
	return monanswer
}

func GetOsdDump(cephconn *Cephconnection) OsdDump {
	monrawanswer := MakeMonQuery(cephconn, map[string]string{"prefix": "osd dump", "format": "json"})
	var monanswer OsdDump
	if err := json.Unmarshal([]byte(monrawanswer), &monanswer); err != nil {
		log.Fatalf("Can't parse monitor answer. Error: %v", err)
	}
	return monanswer
}

func GetCrushHostBuckets(buckets []Bucket, itemid int64) []Bucket {
	var rootbuckets []Bucket
	for _, bucket := range buckets {
		if bucket.ID == itemid {
			if bucket.TypeName == "host" {
				rootbuckets = append(rootbuckets, bucket)
				for _, item := range bucket.Items {
					result := GetCrushHostBuckets(buckets, item.ID)
					for _, it := range result {
						rootbuckets = append(rootbuckets, it)
					}
				}
			} else {
				for _, item := range bucket.Items {
					result := GetCrushHostBuckets(buckets, item.ID)
					for _, it := range result {
						rootbuckets = append(rootbuckets, it)
					}
				}
			}
		}
	}
	return rootbuckets
}

func GetOsdForLocations(params Params, osdcrushdump OsdCrushDump, osddump OsdDump, poolinfo Poolinfo) map[string][]Device {
	var crushrule int64
	var crushrulename string
	for _, pool := range osddump.Pools {
		if pool.Pool == poolinfo.PoolId {
			crushrule = pool.CrushRule
		}
	}
	var rootid int64
	for _, rule := range osdcrushdump.Rules {
		if rule.RuleID == crushrule {
			crushrulename = rule.RuleName
			for _, step := range rule.Steps {
				if step.Op == "take" {
					rootid = step.Item
				}
			}
		}
	}

	osdhosts := make(map[string][]Device)
	bucketitems := GetCrushHostBuckets(osdcrushdump.Buckets, rootid)
	if params.define != "" {
		if strings.HasPrefix(params.define, "osd.") {
			for _, hostbucket := range bucketitems {
				for _, item := range hostbucket.Items {
					for _, device := range osdcrushdump.Devices {
						if device.ID == item.ID && params.define == device.Name {
							osdhosts[hostbucket.Name] = append(osdhosts[hostbucket.Name], device)
						}
					}
				}
			}
			if len(bucketitems) == 0 {
				log.Fatalf("Defined osd not exist in root for rule: %v pool: %v.\nYou should define osd like osd.X",
					crushrulename, poolinfo.Pool)
			}
		} else {
			for _, hostbucket := range bucketitems {
				if strings.HasPrefix(hostbucket.Name, params.define) {
					for _, item := range hostbucket.Items {
						for _, device := range osdcrushdump.Devices {
							if device.ID == item.ID {
								osdhosts[hostbucket.Name] = append(osdhosts[hostbucket.Name], device)
							}
						}
					}
				}
			}
			if len(bucketitems) == 0 {
				log.Fatalf("Defined host not exist in root for rule: %v pool: %v", crushrulename, poolinfo.Pool)
			}
		}
	} else {
		for _, hostbucket := range bucketitems {
			for _, item := range hostbucket.Items {
				for _, device := range osdcrushdump.Devices {
					if device.ID == item.ID {
						osdhosts[hostbucket.Name] = append(osdhosts[hostbucket.Name], device)
					}
				}
			}
		}
		if len(bucketitems) == 0 {
			log.Fatalf("Osd not exist in root for rule: %v pool: %v", crushrulename, poolinfo.Pool)
		}
	}
	return osdhosts
}

func main() {
	params := Route()
	cephconn := connectioninit(params)
	defer cephconn.conn.Shutdown()

	// https://tracker.ceph.com/issues/24114
	time.Sleep(time.Millisecond * 100)

	var buffs [][]byte
	for i := 0; i < 2*params.threadsCount; i++ {
		buffs = append(buffs, make([]byte, params.blocksize))
	}

	for num := range buffs {
		_, err := rand.Read(buffs[num])
		if err != nil {
			log.Fatalln(err)
		}
	}
	poolinfo := GetPoolSize(cephconn, params)
	if poolinfo.Size != 1 {
		log.Fatalf("Pool size must be 1. Current size for pool %v is %v. Don't forget that it must be useless pool (not production). Do:\n # ceph osd pool set %v min_size 1\n # ceph osd pool set %v size 1",
			poolinfo.Pool, poolinfo.Size, poolinfo.Pool, poolinfo.Pool)
	}

	//placementGroups := GetPgByPool(cephconn, params)
	//for _, value := range placementGroups {
	//	log.Printf("%+v\n", value)
	//}
	crushosddump := GetOsdCrushDump(cephconn)
	osddump := GetOsdDump(cephconn)
	osddevices := GetOsdForLocations(params, crushosddump, osddump, poolinfo)
	log.Printf("%+v", osddevices)

}

//TODO получить список PG, если не все находятся на нужных OSD выкинуть эксепшн.

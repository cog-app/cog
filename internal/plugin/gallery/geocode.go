package gallery

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// City represents a city from the embedded GeoNames database.
type City struct {
	Name      string
	Region    string
	Country   string
	Latitude  float64
	Longitude float64
}

// Geocoder performs reverse geocoding using an in-memory city database.
type Geocoder struct {
	cities []City
}

// NewGeocoder creates a geocoder from a CSV reader.
// Expected columns: name, region, country, latitude, longitude.
func NewGeocoder(r io.Reader) (*Geocoder, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = 5
	cr.LazyQuotes = true

	var cities []City
	for {
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read city csv: %w", err)
		}
		var lat, lon float64
		fmt.Sscanf(record[3], "%f", &lat)
		fmt.Sscanf(record[4], "%f", &lon)
		cities = append(cities, City{
			Name:      record[0],
			Region:    record[1],
			Country:   record[2],
			Latitude:  lat,
			Longitude: lon,
		})
	}

	// Sort by latitude for faster nearest-neighbor search.
	sort.Slice(cities, func(i, j int) bool {
		return cities[i].Latitude < cities[j].Latitude
	})

	return &Geocoder{cities: cities}, nil
}

// Reverse finds the nearest city to the given coordinates.
// Returns city name, region, country, and place display name.
func (g *Geocoder) Reverse(lat, lon float64) (city, region, country, placeName string) {
	if len(g.cities) == 0 {
		return "", "", "", ""
	}

	// Binary search to find approximate latitude band, then search within ±2 degrees.
	idx := sort.Search(len(g.cities), func(i int) bool {
		return g.cities[i].Latitude >= lat-2
	})

	bestDist := math.MaxFloat64
	var best *City
	for i := idx; i < len(g.cities) && g.cities[i].Latitude <= lat+2; i++ {
		d := haversine(lat, lon, g.cities[i].Latitude, g.cities[i].Longitude)
		if d < bestDist {
			bestDist = d
			best = &g.cities[i]
		}
	}

	if best == nil {
		return "", "", "", ""
	}

	city = best.Name
	region = best.Region
	country = best.Country

	var parts []string
	if city != "" {
		parts = append(parts, city)
	}
	if region != "" && region != city {
		parts = append(parts, region)
	}
	if country != "" {
		parts = append(parts, country)
	}
	placeName = strings.Join(parts, ", ")

	return city, region, country, placeName
}

// haversine computes the great-circle distance in km between two lat/lon points.
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371 // Earth radius in km
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// defaultCities is a small built-in city database covering major world cities.
// A real deployment would embed a larger GeoNames dataset.
var defaultCities = []City{
	{"New York", "New York", "US", 40.7128, -74.0060},
	{"Los Angeles", "California", "US", 34.0522, -118.2437},
	{"Chicago", "Illinois", "US", 41.8781, -87.6298},
	{"Houston", "Texas", "US", 29.7604, -95.3698},
	{"Phoenix", "Arizona", "US", 33.4484, -112.0740},
	{"San Francisco", "California", "US", 37.7749, -122.4194},
	{"Seattle", "Washington", "US", 47.6062, -122.3321},
	{"Denver", "Colorado", "US", 39.7392, -104.9903},
	{"Miami", "Florida", "US", 25.7617, -80.1918},
	{"Portland", "Oregon", "US", 45.5152, -122.6784},
	{"Boston", "Massachusetts", "US", 42.3601, -71.0589},
	{"Atlanta", "Georgia", "US", 33.7490, -84.3880},
	{"Dallas", "Texas", "US", 32.7767, -96.7970},
	{"Washington", "District of Columbia", "US", 38.9072, -77.0369},
	{"Las Vegas", "Nevada", "US", 36.1699, -115.1398},
	{"Nashville", "Tennessee", "US", 36.1627, -86.7816},
	{"Austin", "Texas", "US", 30.2672, -97.7431},
	{"London", "England", "GB", 51.5074, -0.1278},
	{"Paris", "Île-de-France", "FR", 48.8566, 2.3522},
	{"Berlin", "Berlin", "DE", 52.5200, 13.4050},
	{"Madrid", "Madrid", "ES", 40.4168, -3.7038},
	{"Rome", "Lazio", "IT", 41.9028, 12.4964},
	{"Amsterdam", "North Holland", "NL", 52.3676, 4.9041},
	{"Vienna", "Vienna", "AT", 48.2082, 16.3738},
	{"Prague", "Prague", "CZ", 50.0755, 14.4378},
	{"Barcelona", "Catalonia", "ES", 41.3874, 2.1686},
	{"Munich", "Bavaria", "DE", 48.1351, 11.5820},
	{"Dublin", "Leinster", "IE", 53.3498, -6.2603},
	{"Lisbon", "Lisbon", "PT", 38.7223, -9.1393},
	{"Stockholm", "Stockholm", "SE", 59.3293, 18.0686},
	{"Copenhagen", "Capital Region", "DK", 55.6761, 12.5683},
	{"Oslo", "Oslo", "NO", 59.9139, 10.7522},
	{"Helsinki", "Uusimaa", "FI", 60.1699, 24.9384},
	{"Warsaw", "Masovia", "PL", 52.2297, 21.0122},
	{"Budapest", "Budapest", "HU", 47.4979, 19.0402},
	{"Zurich", "Zurich", "CH", 47.3769, 8.5417},
	{"Brussels", "Brussels", "BE", 50.8503, 4.3517},
	{"Tokyo", "Tokyo", "JP", 35.6762, 139.6503},
	{"Beijing", "Beijing", "CN", 39.9042, 116.4074},
	{"Shanghai", "Shanghai", "CN", 31.2304, 121.4737},
	{"Seoul", "Seoul", "KR", 37.5665, 126.9780},
	{"Singapore", "Singapore", "SG", 1.3521, 103.8198},
	{"Hong Kong", "Hong Kong", "HK", 22.3193, 114.1694},
	{"Bangkok", "Bangkok", "TH", 13.7563, 100.5018},
	{"Mumbai", "Maharashtra", "IN", 19.0760, 72.8777},
	{"Delhi", "Delhi", "IN", 28.7041, 77.1025},
	{"Dubai", "Dubai", "AE", 25.2048, 55.2708},
	{"Istanbul", "Istanbul", "TR", 41.0082, 28.9784},
	{"Cairo", "Cairo", "EG", 30.0444, 31.2357},
	{"Sydney", "New South Wales", "AU", -33.8688, 151.2093},
	{"Melbourne", "Victoria", "AU", -37.8136, 144.9631},
	{"Auckland", "Auckland", "NZ", -36.8485, 174.7633},
	{"São Paulo", "São Paulo", "BR", -23.5505, -46.6333},
	{"Rio de Janeiro", "Rio de Janeiro", "BR", -22.9068, -43.1729},
	{"Buenos Aires", "Buenos Aires", "AR", -34.6037, -58.3816},
	{"Mexico City", "Mexico City", "MX", 19.4326, -99.1332},
	{"Toronto", "Ontario", "CA", 43.6532, -79.3832},
	{"Vancouver", "British Columbia", "CA", 49.2827, -123.1207},
	{"Montreal", "Quebec", "CA", 45.5017, -73.5673},
	{"Cape Town", "Western Cape", "ZA", -33.9249, 18.4241},
	{"Nairobi", "Nairobi", "KE", -1.2921, 36.8219},
	{"Lagos", "Lagos", "NG", 6.5244, 3.3792},
	{"Moscow", "Moscow", "RU", 55.7558, 37.6173},
	{"Honolulu", "Hawaii", "US", 21.3069, -157.8583},
	{"Anchorage", "Alaska", "US", 61.2181, -149.9003},
	{"Reykjavik", "Capital Region", "IS", 64.1466, -21.9426},
}

// NewDefaultGeocoder creates a geocoder with the built-in city database.
func NewDefaultGeocoder() *Geocoder {
	cities := make([]City, len(defaultCities))
	copy(cities, defaultCities)
	sort.Slice(cities, func(i, j int) bool {
		return cities[i].Latitude < cities[j].Latitude
	})
	return &Geocoder{cities: cities}
}

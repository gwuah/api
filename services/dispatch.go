package services

import (
	"encoding/json"
	"log"
	"sort"
	"time"

	"github.com/go-redis/redis"
	"github.com/gwuah/postmates/database/models"
	"github.com/gwuah/postmates/lib/ws"
	"github.com/gwuah/postmates/shared"
	"github.com/gwuah/postmates/utils/geo"
)

func (s *Services) HandleLocationUpdate(params shared.UserLocationUpdate) error {

	switch params.State {
	case models.AwaitingDispatch:
		_, err := s.indexCourierLocation(params)
		return err
	case models.Dispatched, models.OnTrip:
		_, err := s.indexCourierLocation(params)
		if err != nil {
			return err
		}
		_, err = s.repo.CreateTripPoint(params)
		if err != nil {
			return err
		}
		err = s.relayCoordsToCustomer(params)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Services) relayCoordsToCustomer(params shared.UserLocationUpdate) error {
	delivery, err := s.repo.FindDelivery(params.DeliveryId, false)
	if err != nil {
		return err
	}

	redisCourier, err := s.repo.GetCourierFromRedis(string(params.Id))
	if err != nil {
		return err
	}

	duration, distance, err := s.eta.GMAPS__getDistanceAndDuration1to1(shared.Coord{
		Latitude:  redisCourier.Latitude,
		Longitude: redisCourier.Longitude,
	}, shared.Coord{
		Latitude:  delivery.OriginLatitude,
		Longitude: delivery.OriginLongitude,
	})

	if err != nil {
		return nil
	}

	data, err := json.Marshal(shared.CourierLocation{
		Meta: shared.Meta{
			Type: "CourierLocationUpdate",
		},
		Coord:            redisCourier.Coord,
		DistanceToPickup: float64(distance),
		DurationToPickup: float64(duration),
	})

	if err != nil {
		return nil
	}

	if customerConn := s.hub.GetCustomer(delivery.CustomerID); customerConn != nil {
		customerConn.SendMessage(data)
	}

	return nil
}

func (s *Services) indexCourierLocation(param shared.UserLocationUpdate) (*shared.User, error) {
	newIndex := geo.CoordToIndex(param.Coord)

	courier, err := s.repo.GetCourierFromRedis(param.Id)

	if err == redis.Nil {
		courier = &shared.User{
			Id: param.Id,
		}
	}

	if err != redis.Nil && err != nil {
		return nil, err
	}

	oldIndex := courier.LastKnownIndex

	courier.Coord = param.Coord
	courier.LastKnownIndex = newIndex

	err = s.repo.InsertCourierIntoRedis(courier)

	if err != nil {
		return nil, err
	}

	if oldIndex != newIndex {
		err = s.repo.RemoveCourierFromIndex(oldIndex, courier)
		if err != nil {
			return nil, err
		}

		err = s.repo.InsertCourierIntoIndex(newIndex, courier)
		if err != nil {
			return nil, err
		}

	}

	return courier, nil
}

func (s *Services) GetClosestCouriers(destination shared.Coord, steps int) ([]shared.CourierWithEta, error) {
	var e []shared.CourierWithEta

	rings := geo.GetRingsFromOrigin(destination, steps)

	couriersIds := []string{}

	for _, index := range rings {
		ids, err := s.repo.GetCouriersInIndex(index)

		if err != nil {
			log.Printf("failed to load couriers in courier_index %d", index)
			continue
		}

		if len(ids) > 0 {
			couriersIds = append(couriersIds, ids...)
		}
	}

	if len(couriersIds) == 0 {
		return e, nil
	}

	couriers, err := s.repo.GetAllCouriers(couriersIds)

	if err != nil {
		return e, err
	}

	origins := []shared.Coord{}

	for _, courier := range couriers {
		origins = append(origins, shared.Coord{
			Latitude:  courier.Latitude,
			Longitude: courier.Longitude,
		})
	}

	response, err := s.eta.GMAPS__getDistanceAndDurationManyTo1(origins, destination)

	if err != nil {
		return e, err
	}

	for key, dt := range response.Rows {
		courier := couriers[key]
		e = append(e, shared.CourierWithEta{
			Courier:  courier,
			Duration: dt.Elements[0].Duration.Minutes(),
			Distance: float64(dt.Elements[0].Distance.Meters),
		})
	}

	sort.Slice(e, func(i, j int) bool {
		return e[i].Duration < e[j].Duration
	})

	return e, nil

}

func (s *Services) DispatchDelivery(data shared.DeliveryRequest, delivery *models.Delivery, ws *ws.WSConnection) error {

	foundCourierForOrder := false

	if s.hub.GetSize("couriers") == 0 {
		res, err := json.Marshal(shared.NoCourierAvailable{
			Meta: shared.Meta{
				Type: "NoCourierAvailable",
			},
			Message: "there are no couriers available",
		})

		if err != nil {
			return err
		}

		ws.SendMessage(res)

		return nil
	}

dispatchLogic:

	e, err := s.GetClosestCouriers(data.Origin, 2)

	if err != nil {
		return nil
	}

	delivery, err = s.repo.FindDelivery(delivery.ID, true)
	if err != nil {
		return nil
	}

	ticker := time.NewTicker(5 * time.Second)

courierLoop:
	for _, courier := range e {
		conn := s.hub.GetCourier(courier.Courier.Id)
		if conn == nil {
			continue
		}

		convertedDeliveryRequest, err := json.Marshal(shared.NewDelivery{
			Meta: shared.Meta{
				Type: "NewDelivery",
			},
			Delivery:         delivery,
			DistanceToPickup: courier.Distance,
			DurationToPickup: courier.Duration,
		})

		if err != nil {
			return nil
		}

		conn.SendMessage(convertedDeliveryRequest)

		select {
		case <-ticker.C:
			// move to next courier in queue
		case <-conn.DeliveryAcceptanceAck:
			// delivery has been accepted, exit
			ticker.Stop()
			foundCourierForOrder = true
			break courierLoop
		}

	}

	if !foundCourierForOrder {
		goto dispatchLogic
	}

	return nil
}

export type RoutingWeightKey = 'price' | 'efficiency' | 'quality' | 'balance'

export interface RoutingWeightValues {
  /** Contribution of estimated request price to route scoring, in percent. */
  price: number
  /** Contribution of first-token latency, response latency, and throughput, in percent. */
  efficiency: number
  /** Contribution of success and cache quality, in percent. */
  quality: number
  /** Share reserved for correcting recent route distribution, in percent. */
  balance: number
}

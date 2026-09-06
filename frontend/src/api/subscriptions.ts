import { getApp } from './runtime'

export interface SubscriptionStatus { provider: string; connected: boolean; connecting: boolean; available: boolean; message?: string }
export interface SubscriptionLoginResponse { authorization_url: string }

export async function getSubscriptionStatuses(): Promise<SubscriptionStatus[]> { const result = await getApp().GetSubscriptionStatuses(); if (!Array.isArray(result)) throw new Error('invalid subscription status response'); return result as SubscriptionStatus[] }
export async function connectSubscription(provider: string): Promise<SubscriptionLoginResponse> { const result = await getApp().ConnectSubscription(provider); if (!result || typeof result.authorization_url !== 'string') throw new Error('invalid subscription login response'); return result as SubscriptionLoginResponse }
export async function cancelSubscriptionLogin(provider: string): Promise<void> { await getApp().CancelSubscriptionLogin(provider) }
export async function logoutSubscription(provider: string): Promise<void> { await getApp().LogoutSubscription(provider) }

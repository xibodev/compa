import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  ErrorComponent,
  RouterProvider,
  createRouter,
} from "@tanstack/react-router"
import { StrictMode } from "react"
import ReactDOM from "react-dom/client"

import { isClientError } from "./api/http"
import { AppProviders } from "./app-providers"
import "./i18n"
import "./index.css"
import { routeTree } from "./routeTree.gen"

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // One retry covers a blip; a 4xx answer will not change on retry, so it
      // shows at once instead of after several seconds of backoff.
      retry: (failureCount, error) => !isClientError(error) && failureCount < 1,
      // A refetch on focus would replace the data a form is being edited from.
      refetchOnWindowFocus: false,
    },
  },
})

const router = createRouter({
  routeTree,
  context: {
    queryClient,
  },
  // Each page catches its own render errors, so the header and sidebar stay
  // usable when one page breaks.
  defaultErrorComponent: ErrorComponent,
})

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router
  }
}

const rootElement = document.getElementById("root")!
if (!rootElement.innerHTML) {
  const root = ReactDOM.createRoot(rootElement)
  root.render(
    <StrictMode>
      <AppProviders>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </AppProviders>
    </StrictMode>,
  )
}

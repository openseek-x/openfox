import React from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router-dom";

import {
  App,
  InstructionNavigationProvider,
  RouteErrorPage,
} from "@openfox/app";

import "@openfox/app/styles.css";

const router = createBrowserRouter([
  {
    path: "*",
    errorElement: <RouteErrorPage />,
    element: (
      <InstructionNavigationProvider>
        <App />
      </InstructionNavigationProvider>
    ),
  },
]);

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>,
);
